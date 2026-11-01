#!/bin/sh
# Status queries reconcile records with real Linux mounts.
test_description='tiler status filters and renders layered sessions'
. "$(dirname "$0")/lib-tiler.sh"
src_dir=$HOME/src/app
s_dir=$HOME/work/status
s2_dir=$HOME/work/status2
orph_dir=$HOME/work/orph
if test -n "$tiler_bin"
then
	test_atexit '
		for _m in "$s_dir" "$s2_dir" "$orph_dir"
		do
			"$tiler_bin" umount "$_m" >/dev/null 2>&1 ||
			"$fusermount3_bin" -uz "$_m" >/dev/null 2>&1 || true
		done
	'
fi

test_expect_success TILER 'empty and unrelated directory queries succeed' '
	mkdir -p "$HOME/work/uncovered" &&
	"$tiler_bin" status >empty.out 2>empty.err &&
	test_grep "^no sessions recorded$" empty.out &&
	"$tiler_bin" status "$HOME/work/uncovered" --json >empty.json &&
	test_grep -F "\"sessions\": []" empty.json &&
	test_grep -F "\"orphan_views\": []" empty.json
'

test_expect_success TILER,LINUX 'unmounted active records remain visible with missing mount diagnostics' '
	"$tiler_bin" session new idle &&
	test_when_finished '\''"$tiler_bin" session close idle && "$tiler_bin" session remove idle'\'' &&
	"$tiler_bin" status --active -s idle >idle.out &&
	test_grep "^idle · active · missing-mounts$" idle.out &&
	"$tiler_bin" status --no-active -s idle --json >idle.json &&
	test_grep -F "\"sessions\": []" idle.json
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'healthy sessions show mount details and current markers' '
	mkdir -p "$src_dir/sub/deep" "$s_dir" "$s2_dir" &&
	echo hello >"$src_dir/README.txt" &&
	"$tiler_bin" build --image tmp/status "$src_dir" >build.out &&
	full=$(cut -d" " -f3 build.out) &&
	"$tiler_bin" session new dev &&
	"$tiler_bin" mount --session dev tmp/status "$s_dir" &&
	"$tiler_bin" mount --session dev tmp/status "$s2_dir" &&
	TILER_SESSION_ID=dev "$tiler_bin" status >live.out &&
	test_grep "^dev · active · current$" live.out &&
	test_grep "📁 tmp/status:.*\[rw · mounted · healthy\]" live.out &&
	test_grep "upper.*runtime/m1/upper" live.out &&
	test_grep "lower.*highest priority first" live.out &&
	TILER_SESSION_ID=bogus "$tiler_bin" status >other.out &&
	test_grep ! " · current" other.out
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'directory filters and detail aliases agree without renewing activity' '
	cp "$HOME/.tiler/logs/sessions/dev.log" activity.before &&
	"$tiler_bin" status "$HOME/work" >parent.out &&
	"$tiler_bin" status "$s_dir/sub/deep" >child.out &&
	test_cmp parent.out child.out &&
	"$tiler_bin" status -s dev >selected.out &&
	"$tiler_bin" session status -s dev >alias.out &&
	"$tiler_bin" session show dev >show.out &&
	"$tiler_bin" session list -v >verbose.out &&
	test_cmp selected.out alias.out &&
	test_cmp selected.out show.out &&
	test_cmp selected.out verbose.out &&
	"$tiler_bin" session list --json >brief.json &&
	"$tiler_bin" session list -v --json >verbose.json &&
	test_cmp brief.json verbose.json &&
	test_grep "\"last_activity_at\": \"[0-9]" brief.json &&
	"$tiler_bin" session list >brief.out &&
	test_grep "CLOSED.*LAST ACTIVITY.*RM" brief.out &&
	test_cmp activity.before "$HOME/.tiler/logs/sessions/dev.log" &&
	test_expect_code 1 "$tiler_bin" status "$s_dir/README.txt" >file.out 2>file.err &&
	test_grep "directory required" file.err
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'layered image filters find full middle and tip versions' '
	echo first >"$s_dir/first.txt" &&
	(cd "$s_dir" && "$tiler_bin" build --incremental) >first.out &&
	middle=$(cut -d" " -f3 first.out) &&
	"$tiler_bin" session close dev && "$tiler_bin" session open dev &&
	echo second >"$s_dir/second.txt" &&
	(cd "$s_dir" && "$tiler_bin" build --incremental) >second.out &&
	tip=$(cut -d" " -f3 second.out) &&
	"$tiler_bin" session close dev && "$tiler_bin" session open dev &&
	for version in "$full" "$middle" "$tip"
	do
		"$tiler_bin" status "$HOME/work" -s dev -i "tmp/status:$version" --json >layer.json &&
		test_grep -F "\"name\": \"dev\"" layer.json &&
		test_grep -F "\"version\": \"$version\"" layer.json || return 1
	done &&
	"$tiler_bin" status -s dev >layers.out &&
	test_grep "full.*runtime/m1/lower-2" layers.out &&
	test_grep "incremental.*runtime/m1/lower-1" layers.out &&
	test_grep "incremental.*runtime/m1/lower-0" layers.out &&
	"$tiler_bin" status -i tmp/status --json >all.json &&
	test_grep -F "\"name\": \"dev\"" all.json
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'active filtering retains mounted sessions and excludes them from the inverse' '
	"$tiler_bin" status --active -s dev --json >active.json &&
	test_grep -F "\"active\": true" active.json &&
	"$tiler_bin" status --no-active -s dev --json >inactive.json &&
	test_grep -F "\"sessions\": []" inactive.json
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN,KILL_VIEW 'a killed view is stale while its peer stays healthy' '
	kill_view "$s2_dir" &&
	wait_for_stale_view dev stale.json &&
	"$tiler_bin" status -s dev >stale.out &&
	test_grep "\[rw · mounted · healthy\]" stale.out &&
	test_grep "\[rw · mounted · stale\]" stale.out &&
	"$tiler_bin" status -s dev --json >stale.json &&
	test_grep -F "\"health\": \"stale\"" stale.json
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'closed sessions retain mount history without live views' '
	"$tiler_bin" session close dev &&
	"$tiler_bin" status -s dev >closed.out &&
	test_grep "^dev · closed$" closed.out &&
	test_grep "Closed:.*Last activity:" closed.out &&
	test_grep "\[rw · unmounted\]" closed.out &&
	"$tiler_bin" status -s dev --json >closed.json &&
	test_grep -F "\"actions\": [" closed.json &&
	test_grep -F "\"orphan_views\": []" closed.json
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'internal layers remain active after user views disappear' '
	"$tiler_bin" session open dev &&
	"$fusermount3_bin" -u "$s_dir" &&
	"$fusermount3_bin" -u "$s2_dir" &&
	"$tiler_bin" status --active -s dev >residual.out &&
	test_grep "^dev · active · residual-mounts$" residual.out &&
	"$tiler_bin" session close dev &&
	"$tiler_bin" status --no-active -s dev --json >inactive.json &&
	test_grep -F "\"name\": \"dev\"" inactive.json &&
	test_grep -F "\"active\": false" inactive.json
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'lost records leave orphans outside verbose session lists' '
	mkdir -p "$orph_dir" &&
	"$tiler_bin" mount tmp/status "$orph_dir" >mount.out &&
	auto=$(sed -n "s/^new session created: //p" mount.out) &&
	test -n "$auto" &&
	rm -f "$HOME/.tiler/sessions/$auto/session.json" &&
	"$tiler_bin" status >orphan.out &&
	test_grep "^orphan .*fuse.fuse-overlayfs " orphan.out &&
	"$tiler_bin" session list -v >list.out &&
	test_grep ! "^orphan " list.out
'
test_done
