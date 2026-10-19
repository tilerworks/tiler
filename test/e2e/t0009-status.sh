#!/bin/sh
#
# tiler end-to-end test: the status command over real mounts.
#
# Companion to t0006 (named session lifecycle), t0007 (crash recovery)
# and t0008 (implicit auto-* sessions). Those drive `session`; this case
# drives `status`, the read model that reconciles every record against
# the live mount table. It covers both forms of the command:
#
#   1. no path -- the overview: an empty store, a healthy session marked
#      current by TILER_SESSION_ID, a current marker that matches no
#      record, a closed session with no live views, and a live view
#      whose record was lost reported as an orphan,
#   2. a path -- the reverse lookup: a deep path and the mountpoint both
#      resolve to the covering session, an uncovered path is an error,
#      and a killed view resolves stale.
#
# Each form is asserted in both the human text and the --json document.
# It mirrors the status scenario of the out-of-repo tiler-demo S04 and
# tiler-e2e s03 steps, re-expressed in the vendored git-test-lib
# framework so `make e2e` runs it.
#
# A real mount needs a writable /dev/fuse plus the squashfs and overlay
# toolchain that `tiler doctor` treats as fail-level, and the stale case
# needs pgrep to find the daemon it kills. The FUSE_TOOLCHAIN and
# KILL_VIEW prereqs below model exactly that, so the case skips cleanly
# -- rather than false-fails -- on a host without them. The two opening
# assertions (empty overview, uncovered path) need only the binary.
#
# Scope guard: this case asserts the status read model only. It does not
# cover the session lifecycle operations (t0006), the open-driven crash
# recovery (t0007), implicit-session teardown (t0008), the `locked`
# marker (needs a concurrent lock holder; unit-tested only), git-backed
# images, or split storage.

test_description='tiler status reconciles sessions with the real mount state'

# Resolve paths against this script's location BEFORE sourcing the
# framework: test-lib.sh moves the working directory into a per-run
# trash directory and repoints HOME there.
tiler_case_dir=$(cd "$(dirname "$0")" && pwd)
TEST_LIB_DIRECTORY=$(cd "$tiler_case_dir/../git-test-lib" && pwd)
export TEST_LIB_DIRECTORY
TEST_DIRECTORY=$tiler_case_dir
export TEST_DIRECTORY

. "$TEST_LIB_DIRECTORY/test-lib.sh"

# Prefer the repository build; fall back to a tiler found on PATH.
tiler_repo_root=$(cd "$tiler_case_dir/../.." && pwd)
tiler_bin=$tiler_repo_root/bin/tiler
if ! test -x "$tiler_bin"
then
	tiler_bin=$(command -v tiler) || tiler_bin=
fi
if test -n "$tiler_bin"
then
	test_set_prereq TILER
fi

# A real mount needs the FUSE device and the squashfs/overlay toolchain
# (the same set `tiler doctor` reports as fail-level). Model it here so
# the assertions stay real checks when the tools are present and skip
# cleanly when they are not.
test_lazy_prereq FUSE_TOOLCHAIN '
	test -e /dev/fuse &&
	command -v squashfuse_ll >/dev/null 2>&1 &&
	command -v fuse-overlayfs >/dev/null 2>&1 &&
	command -v fusermount3 >/dev/null 2>&1 &&
	command -v mksquashfs >/dev/null 2>&1 &&
	command -v git >/dev/null 2>&1
'

# The stale case SIGKILLs the daemon behind a view, which needs pgrep to
# find it. Gate only that assertion on pgrep so the rest of the mount
# cases still run on a host without it.
test_lazy_prereq KILL_VIEW '
	command -v pgrep >/dev/null 2>&1
'

# HOME is the per-run trash directory, so tiler's default layout
# ($HOME/.tiler/{images,runtime,sessions,cache}) is already sandboxed and
# needs no --*-dir flags. Mount targets must sit OUTSIDE the runtime tree
# and the reserved directories, so keep them under $HOME/work. s_dir and
# s2_dir are two views of one named session (dev); orph_dir is a plain
# mount whose record is deleted to make it an orphan; uncov_dir is never
# mounted and stands in for a path no session covers.
src_dir=$HOME/src/app
s_dir=$HOME/work/status
s2_dir=$HOME/work/status2
orph_dir=$HOME/work/orph
uncov_dir=$HOME/work/uncovered

# kill_view SIGKILLs the FUSE daemon(s) serving one mountpoint, so the
# stale case can simulate a daemon death while the mount table entry
# stays behind (exactly the state `status` must report stale). It matches
# the mountpoint against the full command line (pgrep -f), skips this
# shell, and re-confirms each candidate against /proc/<pid>/cmdline with
# a fixed-string match, so a regex over-match or a path with spaces never
# kills the wrong process. It returns success only when it actually
# killed a daemon. Same helper as t0007.
kill_view() {
	_mp=${1:-}
	if test -z "$_mp"
	then
		echo "usage: kill_view mountpoint" >&2
		return 2
	fi
	_found=0
	for _pid in $(pgrep -f "$_mp" 2>/dev/null)
	do
		test "$_pid" = "$$" && continue
		tr "\0" " " <"/proc/$_pid/cmdline" 2>/dev/null |
			grep -qF "$_mp" || continue
		kill -9 "$_pid" 2>/dev/null && _found=1
	done
	test "$_found" -eq 1
}

# Guarantee every FUSE mount is torn down before the framework removes
# the trash directory, even if an assertion fails, a daemon was killed,
# or the run is interrupted. `tiler umount` is idempotent; fusermount3
# -uz is the fallback for a view whose daemon already died or whose
# record was deleted.
if test -n "$tiler_bin"
then
	test_atexit '
		for _m in "$s_dir" "$s2_dir" "$orph_dir"
		do
			"$tiler_bin" umount "$_m" >/dev/null 2>&1 ||
			fusermount3 -uz "$_m" >/dev/null 2>&1 ||
			true
		done
	'
fi

# git-test-lib eval's each body in the current shell, so the dev session
# and its views persist across the blocks below: build and mount them,
# read them every which way, kill one, close the session, then orphan a
# fresh plain mount.

# With no record and no mount, the overview says so explicitly and the
# json document carries empty arrays. Needs only the binary.
test_expect_success TILER 'status with no sessions reports none in text and json' '
	"$tiler_bin" status >empty.out 2>empty.err &&
	grep -Eq "^no sessions recorded$" empty.out &&
	"$tiler_bin" status --json >emptyj.out 2>emptyj.err &&
	grep -qF "\"current_session\": \"\"" emptyj.out &&
	grep -qF "\"sessions\": []" emptyj.out &&
	grep -qF "\"orphan_views\": []" emptyj.out
'

# A path no mounted view covers is an error, not an empty result, in both
# forms. Needs only the binary: nothing is mounted to query.
test_expect_success TILER 'status of an uncovered path is an error in text and json' '
	mkdir -p "$uncov_dir" &&
	status=0 &&
	{ "$tiler_bin" status "$uncov_dir" >uncov.out 2>uncov.err || status=$?; } &&
	test "$status" -eq 1 &&
	grep -q "no session covers" uncov.err &&
	status=0 &&
	{ "$tiler_bin" status "$uncov_dir" --json >uncovj.out 2>uncovj.err || status=$?; } &&
	test "$status" -eq 1 &&
	grep -q "no session covers" uncovj.err
'

# Build one image and mount it at two views under a named session. With
# TILER_SESSION_ID naming that session, the overview marks it current and
# annotates both live mounts healthy, in text and json.
test_expect_success TILER,FUSE_TOOLCHAIN 'status overview marks a healthy current session' '
	mkdir -p "$src_dir/sub/deep" &&
	echo "hello tiler" >"$src_dir/README.txt" &&
	"$tiler_bin" build --image tmp/status "$src_dir" >build.out 2>build.err &&
	grep -Eq "^full tmp/status [0-9a-f]+" build.out &&
	"$tiler_bin" session new dev >new.out 2>new.err &&
	grep -Eq "^created dev$" new.out &&
	mkdir -p "$s_dir" "$s2_dir" &&
	"$tiler_bin" mount --session dev tmp/status "$s_dir" >m1.out 2>m1.err &&
	grep -Eq "^mounted " m1.out &&
	"$tiler_bin" mount --session dev tmp/status "$s2_dir" >m2.out 2>m2.err &&
	grep -Eq "^mounted " m2.out &&
	TILER_SESSION_ID=dev "$tiler_bin" status >ov.out 2>ov.err &&
	grep -Eq "^dev active created=.* current$" ov.out &&
	grep -Eq "^  1 mount tmp/status .* mode=rw status=mounted health=healthy$" ov.out &&
	grep -Eq "^  2 mount tmp/status .* mode=rw status=mounted health=healthy$" ov.out &&
	TILER_SESSION_ID=dev "$tiler_bin" status --json >ovj.out 2>ovj.err &&
	grep -qF "\"current_session\": \"dev\"" ovj.out &&
	grep -qF "\"current\": true" ovj.out &&
	test "$(grep -cF "\"health\": \"healthy\"" ovj.out)" -eq 2
'

# The environment marker is carried verbatim and may match no record;
# that is not an error. No header is marked current, yet the json
# current_session still echoes the marker.
test_expect_success TILER,FUSE_TOOLCHAIN 'a current marker matching no record is not an error' '
	TILER_SESSION_ID=bogus "$tiler_bin" status >nc.out 2>nc.err &&
	grep -Eq "^dev active created=" nc.out &&
	! grep -Eq " current$" nc.out &&
	TILER_SESSION_ID=bogus "$tiler_bin" status --json >ncj.out 2>ncj.err &&
	grep -qF "\"current_session\": \"bogus\"" ncj.out &&
	grep -qF "\"current\": false" ncj.out
'

# The path form resolves a deep path inside the merged view and the
# mountpoint itself to the covering session. Its action line carries no
# health mark (that lives only in the overview and the json), and a
# healthy view is not stale.
test_expect_success TILER,FUSE_TOOLCHAIN 'status of a path resolves the covering session' '
	"$tiler_bin" status "$s_dir/sub/deep/x.txt" >p1.out 2>p1.err &&
	grep -Eq "^dev active mountpoint=" p1.out &&
	grep -qF "$s_dir" p1.out &&
	! grep -q " stale" p1.out &&
	grep -Eq "^  1 mount tmp/status .* mode=rw status=mounted$" p1.out &&
	"$tiler_bin" status "$s_dir" >p2.out 2>p2.err &&
	grep -Eq "^dev active mountpoint=" p2.out &&
	grep -qF "$s_dir" p2.out &&
	"$tiler_bin" status "$s_dir/sub/deep/x.txt" --json >p3.out 2>p3.err &&
	grep -qF "\"stale\": false" p3.out &&
	grep -qF "\"session\": \"dev\"" p3.out &&
	grep -qF "\"health\": \"healthy\"" p3.out &&
	grep -qF "$s_dir" p3.out
'

# SIGKILL the daemon behind the second view: it goes stale (any access
# returns ENOTCONN). The overview annotates that view stale while its
# peer stays healthy, the path form marks the header stale, and the json
# reports stale with a stale action health.
test_expect_success TILER,FUSE_TOOLCHAIN,KILL_VIEW 'status reports a killed view stale while its peer stays healthy' '
	kill_view "$s2_dir" &&
	sleep 1 &&
	! ls "$s2_dir" >/dev/null 2>&1 &&
	"$tiler_bin" status >ov6.out 2>ov6.err &&
	grep -Eq "^  1 mount tmp/status .* mode=rw status=mounted health=healthy$" ov6.out &&
	grep -Eq "^  2 mount tmp/status .* mode=rw status=mounted health=stale$" ov6.out &&
	"$tiler_bin" status "$s2_dir" >ps6.out 2>ps6.err &&
	grep -Eq " stale$" ps6.out &&
	grep -qF "$s2_dir" ps6.out &&
	grep -Eq "^  2 mount tmp/status .* mode=rw status=mounted$" ps6.out &&
	"$tiler_bin" status "$s2_dir" --json >pj6.out 2>pj6.err &&
	grep -qF "\"stale\": true" pj6.out &&
	grep -qF "\"health\": \"stale\"" pj6.out
'

# close tears both views down (the killed one included) and flips the
# record to closed. The overview then shows the closed header with no
# expanded actions and no orphans, and the json carries an empty action
# list.
test_expect_success TILER,FUSE_TOOLCHAIN 'a closed session shows no live views or orphans' '
	"$tiler_bin" session close dev >cl7.out 2>cl7.err &&
	grep -Eq "^closed dev$" cl7.out &&
	"$tiler_bin" status >ov7.out 2>ov7.err &&
	grep -Eq "^dev closed created=" ov7.out &&
	! grep -q "^  " ov7.out &&
	! grep -q "^orphan " ov7.out &&
	"$tiler_bin" status --json >j7.out 2>j7.err &&
	grep -qF "\"state\": \"closed\"" j7.out &&
	grep -qF "\"actions\": []" j7.out &&
	grep -qF "\"orphan_views\": []" j7.out
'

# A plain mount with no --session mints an implicit auto-* record. Delete
# that record out-of-band while the view stays live: the mount is now
# claimed by no active record, so the overview lists it as an orphan and
# the json carries a non-empty orphan_views naming the view point.
test_expect_success TILER,FUSE_TOOLCHAIN 'status lists a live view whose record was lost as an orphan' '
	mkdir -p "$orph_dir" &&
	"$tiler_bin" mount tmp/status "$orph_dir" >m8.out 2>m8.err &&
	grep -Eq "^mounted " m8.out &&
	"$tiler_bin" session list >l8.out 2>l8.err &&
	auto=$(grep -E "^auto-[^ ]+ active mounts=1$" l8.out | head -1 | cut -d" " -f1) &&
	test -n "$auto" &&
	rm -rf "$HOME/.tiler/sessions/$auto" &&
	"$tiler_bin" status >ov8.out 2>ov8.err &&
	grep -Eq "^orphan " ov8.out &&
	grep -qF "$orph_dir" ov8.out &&
	grep -Eq "^orphan .* fuse\.fuse-overlayfs " ov8.out &&
	"$tiler_bin" status --json >j8.out 2>j8.err &&
	grep -qF "\"point\"" j8.out &&
	grep -qF "$orph_dir" j8.out &&
	grep -qF "\"fstype\": \"fuse.fuse-overlayfs\"" j8.out
'

test_done
