#!/bin/sh
#
# tiler end-to-end test: activity-aware GC and ancestor activity.
#
# Session GC runs with only the tiler binary. Image GC publishes one
# full version with mksquashfs and skips that half when the packer is
# missing. These original cases do not mount, so a host without
# /dev/fuse still runs them; only the graph below needs FUSE.
#
# The case checks the preview contract, one-time initialization, legacy
# session_max_age parsing, and removal of a session's upper data. Parent
# activity and ancestor retention use an isolated squashfs graph below.

test_description='tiler GC initializes activity once and retains referenced ancestors'

. "$(dirname "$0")/lib-tiler.sh"
. "$TEST_DIRECTORY/lib-workflow.sh"

# tiler_bin, TILER, LINUX, and SQUASHFS_PACK come from sharness.d/tiler.sh.

# rewrite_activity sets every record time. GC reads the last line, so
# one old timestamp is enough to make the object eligible.
rewrite_activity() {
	sed "s/\"time\":\"[^\"]*\"/\"time\":\"$2\"/" "$1" >"$1.rewritten" &&
	mv "$1.rewritten" "$1"
}

sessions=$HOME/.tiler/sessions
images=$HOME/.tiler/images
src_dir=$HOME/src/app

test_expect_success TILER 'legacy session age settings stay parseable and do not drive GC' '
	cat >"$HOME/.tiler.yaml" <<-\EOF &&
	version: 1
	session_max_age: 0s
	session_autoclose_stale: true
	EOF
	"$tiler_bin" >config.out 2>config.err &&
	test_grep "^Usage:" config.out &&
	test_grep "session_max_age: 0s" "$HOME/.tiler.yaml" &&
	test_grep "session_autoclose_stale: true" "$HOME/.tiler.yaml" &&
	test_expect_code 2 "$tiler_bin" session gc --expire -1 >neg.out 2>neg.err &&
	test_grep "nonnegative" neg.err &&
	test_expect_code 2 "$tiler_bin" images gc --expire 1.5 >frac.out 2>frac.err &&
	test_grep "nonnegative" frac.err
'

test_expect_success TILER 'session GC previews, initializes once, and then deletes upper data' '
	"$tiler_bin" session new idle >new.out 2>new.err &&
	test_grep "^created idle$" new.out &&
	mkdir -p "$sessions/idle/runtime/m1/upper" &&
	echo keep >"$sessions/idle/runtime/m1/upper/work" &&
	test_grep "session.create" "$HOME/.tiler/logs/sessions/idle.log" &&
	"$tiler_bin" session gc >fresh.out 2>fresh.err &&
	test_grep "No session expired" fresh.out &&
	test_grep ! "Type yes" fresh.err &&
	test_path_is_file "$sessions/idle/runtime/m1/upper/work" &&
	rewrite_activity "$HOME/.tiler/logs/sessions/idle.log" "2020-01-01T00:00:00.000000000Z" &&
	"$tiler_bin" session gc >aged.out 2>aged.err &&
	test_grep "Expired sessions" aged.out &&
	test_grep "  idle  " aged.out &&
	test_grep "runtime/" aged.out &&
	test_grep "Nothing removed" aged.err &&
	printf "yes\n" | "$tiler_bin" session gc >pipe.out 2>pipe.err &&
	test_grep "Nothing removed" pipe.err &&
	test_path_is_file "$sessions/idle/runtime/m1/upper/work" &&
	rm -f "$HOME/.tiler/logs/sessions/idle.log" &&
	"$tiler_bin" session gc --expire 0 >init1.out 2>init1.err &&
	test_grep "No session expired" init1.out &&
	test_grep "kept idle: initialized" init1.err &&
	test_grep "activity.initialize" "$HOME/.tiler/logs/sessions/idle.log" &&
	cp "$HOME/.tiler/logs/sessions/idle.log" idle.before &&
	"$tiler_bin" session gc --expire 0 >init2.out 2>init2.err &&
	test_grep ! initialized init2.err &&
	test_cmp idle.before "$HOME/.tiler/logs/sessions/idle.log" &&
	test_grep "Expired sessions" init2.out &&
	test_grep "Nothing removed" init2.err &&
	test_path_is_file "$sessions/idle/runtime/m1/upper/work" &&
	"$tiler_bin" sessions gc --expire 0 --yes >yes.out 2>yes.err &&
	test_grep "^removed idle$" yes.out &&
	test_grep "removed 1 of 1 approved sessions, kept 0" yes.out &&
	test_path_is_missing "$sessions/idle"
'

test_expect_success TILER 'image GC with an empty library only previews' '
	"$tiler_bin" images gc >empty.out 2>empty.err &&
	test_grep "No image objects eligible" empty.out &&
	test_grep ! "Type yes" empty.err
'

test_expect_success TILER,LINUX,SQUASHFS_PACK,WORKFLOW_SQUASHFS_CLIENT 'image GC previews a full version, initializes once, and then deletes it' '
	mkdir -p "$src_dir" &&
	echo "hello tiler" >"$src_dir/README.txt" &&
	"$tiler_bin" build --image alice/app "$src_dir" >build.out 2>build.err &&
	test_grep -E "^full alice/app [0-9a-f]+" build.out &&
	id=$(cut -d" " -f3 build.out) &&
	test -n "$id" &&
	log=$HOME/.tiler/logs/images/alice/app/$id.log &&
	test_grep "\"event\":\"image.create.full\"" "$log" &&
	test_grep ! "image.dependency.activity" "$log" &&
	"$tiler_bin" images gc --expire 0 >prev.out 2>prev.err &&
	test_grep "Image GC candidates" prev.out &&
	test_grep "expired  alice/app:$id" prev.out &&
	test_grep "external source files are preserved" prev.out &&
	test_grep "Nothing removed" prev.err &&
	test_grep ! "^removed " prev.out &&
	test_path_is_file "$images/alice/app/_objects/$id/image.sqsh" &&
	rm -f "$log" &&
	"$tiler_bin" images gc --expire 0 >i1.out 2>i1.err &&
	test_grep "No image objects eligible" i1.out &&
	test_grep "kept alice/app:$id: initialized" i1.err &&
	test_grep "activity.initialize" "$log" &&
	cp "$log" image.before &&
	"$tiler_bin" images gc --expire 0 >i2.out 2>i2.err &&
	test_grep ! initialized i2.err &&
	test_cmp image.before "$log" &&
	test_grep "expired  alice/app:$id" i2.out &&
	test_grep "Nothing removed" i2.err &&
	test_path_is_file "$images/alice/app/_objects/$id/image.sqsh" &&
	"$tiler_bin" image gc --expire 0 --yes >del.out 2>del.err &&
	test_grep "^removed alice/app:$id$" del.out &&
	test_grep "removed 1 of 1 approved image objects, skipped 0" del.out &&
	test_path_is_missing "$images/alice/app"
'

# This graph owns its project configuration and does not consume the
# mount-free cases above. Its live view produces sibling increments.
graph=$HOME/chain-gc
graph_image=gc/chain
graph_logs=$graph/.tiler/logs/images/$graph_image
graph_objects=$graph/.tiler/images/$graph_image/_objects

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'prepare an independent squashfs graph for activity and GC' '
	workflow_config "$graph" &&
	mkdir -p "$graph/prepare" "$graph/mnt-edit" &&
	printf "graph base\n" >"$graph/prepare/README.txt" &&
	test_atexit '\''
		(
			cd "$graph" || exit
			"$tiler_bin" umount "$graph/mnt-edit" >/dev/null 2>&1 ||
			"$fusermount3_bin" -uz "$graph/mnt-edit" >/dev/null 2>&1 || true
		)
	'\'' &&
	(
		cd "$graph" &&
		"$tiler_bin" build -I "$graph_image" -f --format squashfs prepare >"$HOME/graph-base.out" 2>"$HOME/graph-base.err" &&
		"$tiler_bin" session new activity-edit >"$HOME/graph-session.out" 2>"$HOME/graph-session.err" &&
		"$tiler_bin" mount -s activity-edit "$graph_image" mnt-edit >"$HOME/graph-mount.out" 2>"$HOME/graph-mount.err" &&
		printf "first sibling\n" >mnt-edit/first.txt &&
		(
			cd mnt-edit &&
			"$tiler_bin" build -i --format squashfs >"$HOME/graph-first.out" 2>"$HOME/graph-first.err"
		) &&
		graph_first=$(cut -d" " -f3 "$HOME/graph-first.out") &&
		cp "$graph_logs/$graph_first.log" "$HOME/graph-sibling.before" &&
		printf "second sibling\n" >mnt-edit/second.txt &&
		(
			cd mnt-edit &&
			"$tiler_bin" build -i --format squashfs >"$HOME/graph-second.out" 2>"$HOME/graph-second.err"
		) &&
		printf "independent full\n" >prepare/README.txt &&
		"$tiler_bin" build -I "$graph_image" -f --format squashfs prepare >"$HOME/graph-full.out" 2>"$HOME/graph-full.err"
	) &&
	test_grep -E "^full $graph_image [0-9a-f]{64}$" graph-base.out &&
	test_grep -E "^incr $graph_image [0-9a-f]{64}$" graph-first.out &&
	test_grep -E "^incr $graph_image [0-9a-f]{64}$" graph-second.out &&
	test_grep -E "^full $graph_image [0-9a-f]{64}$" graph-full.out &&
	graph_base=$(cut -d" " -f3 graph-base.out) &&
	graph_first=$(cut -d" " -f3 graph-first.out) &&
	graph_second=$(cut -d" " -f3 graph-second.out) &&
	graph_full=$(cut -d" " -f3 graph-full.out) &&
	test "$graph_first" != "$graph_second" &&
	test "$graph_full" != "$graph_base" &&
	for tiler_child in "$graph_first" "$graph_second"
	do
		test_grep -E "^[[:space:]]*parent: $graph_base$" "$graph_objects/$tiler_child/image.yaml" || return 1
	done
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'incremental activity reaches its parent exactly once and preserves its sibling' '
	tip_log=$graph_logs/$graph_second.log &&
	root_log=$graph_logs/$graph_base.log &&
	sib_log=$graph_logs/$graph_first.log &&
	test_path_is_file "$tip_log" &&
	sed -n "s/.*\"time\":\"\\([^\"]*\\)\".*\"event\":\"image.create.incremental\".*\"phase\":\"succeeded\".*/\\1/p" "$tip_log" >graph-tip-time &&
	test_line_count = 1 graph-tip-time &&
	tip_time=$(cat graph-tip-time) &&
	test -n "$tip_time" &&
	test_grep "\"time\":\"$tip_time\".*\"source_version\":\"$graph_second\".*\"source_event\":\"image.create.incremental\"" "$root_log" &&
	grep "\"source_version\":\"$graph_second\"" "$root_log" >graph-source-events &&
	test_line_count = 1 graph-source-events &&
	test_cmp graph-sibling.before "$sib_log" &&
	test_grep ! "\"source_version\":\"$graph_second\"" "$sib_log" &&
	test_grep "\"event\":\"image.create.full\"" "$root_log"
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'GC initializes a missing child log once without refreshing its sibling' '
	rm "$graph_logs/$graph_second.log" &&
	(
		cd "$graph" &&
		"$tiler_bin" images gc --expire 0 2>"$HOME/graph-init1.err"
	) >graph-init1.out &&
	test_grep "kept $graph_image:$graph_second: initialized" graph-init1.err &&
	test_grep "kept $graph_image:$graph_base: initialized" graph-init1.err &&
	test_grep ! "kept $graph_image:$graph_first: initialized" graph-init1.err &&
	sed -n "s/.*\"time\":\"\\([^\"]*\\)\".*\"event\":\"activity.initialize\".*/\\1/p" "$graph_logs/$graph_second.log" >graph-init-time &&
	test_line_count = 1 graph-init-time &&
	init_time=$(cat graph-init-time) &&
	test -n "$init_time" &&
	test_grep "\"time\":\"$init_time\".*\"source_version\":\"$graph_second\".*\"source_event\":\"activity.initialize\"" "$graph_logs/$graph_base.log" &&
	test_cmp graph-sibling.before "$graph_logs/$graph_first.log" &&
	test_grep ! "activity.initialize" "$graph_logs/$graph_first.log" &&
	cp "$graph_logs/$graph_base.log" graph-base.before &&
	cp "$graph_logs/$graph_second.log" graph-second.before &&
	(
		cd "$graph" &&
		"$tiler_bin" images gc --expire 0 2>"$HOME/graph-init2.err"
	) >graph-init2.out &&
	test_grep ! initialized graph-init2.err &&
	test_cmp graph-base.before "$graph_logs/$graph_base.log" &&
	test_cmp graph-second.before "$graph_logs/$graph_second.log"
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'GC retains ancestors of a recent child and removes expired branches' '
	(
		cd "$graph" &&
		"$tiler_bin" session close activity-edit >"$HOME/graph-close.out" 2>"$HOME/graph-close.err" &&
		"$tiler_bin" session remove activity-edit >"$HOME/graph-remove.out" 2>"$HOME/graph-remove.err"
	) &&
	test_path_is_missing "$graph/.tiler/sessions/activity-edit" &&
	find "$graph/mnt-edit" -mindepth 1 >graph-unmounted &&
	test_must_be_empty graph-unmounted &&
	test_grep ! "image.dependency.activity" "$graph_logs/$graph_full.log" &&
	for tiler_old in "$graph_base" "$graph_first" "$graph_full"
	do
		rewrite_activity "$graph_logs/$tiler_old.log" "2020-01-01T00:00:00.000000000Z" || return 1
	done &&
	rewrite_activity "$graph_logs/$graph_second.log" "2099-01-01T00:00:00.000000000Z" &&
	(
		cd "$graph" &&
		"$tiler_bin" images gc --expire 0 --yes 2>"$HOME/graph-anc.err"
	) >graph-anc.out &&
	for tiler_old in "$graph_first" "$graph_full"
	do
		test_grep "^removed $graph_image:$tiler_old$" graph-anc.out &&
		test_path_is_missing "$graph_objects/$tiler_old" &&
		test_path_is_missing "$graph_logs/$tiler_old.log" &&
		test_path_is_missing "$graph_logs/$tiler_old.log.lock" || return 1
	done &&
	for tiler_kept in "$graph_base" "$graph_second"
	do
		test_grep ! "removed $graph_image:$tiler_kept" graph-anc.out &&
		test_path_is_dir "$graph_objects/$tiler_kept" &&
		test_path_is_file "$graph_logs/$tiler_kept.log" || return 1
	done
'

test_done
