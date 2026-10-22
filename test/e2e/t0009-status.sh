#!/bin/sh
#
# tiler end-to-end test: the status command over real mounts.
#
# Companion to t0006 (named session lifecycle), t0007 (crash recovery)
# and t0008 (generated sessions). Those drive `session`; this case
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
# needs pgrep to find the daemon it kills. FUSE_TOOLCHAIN and KILL_VIEW from
# sharness.d/tiler.sh model exactly that, so the case skips cleanly
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

# tiler_bin, TILER, LINUX, FUSE_TOOLCHAIN, KILL_VIEW, and kill_view
# come from sharness.d/tiler.sh.

# HOME is the per-run trash directory, so tiler's default layout
# ($HOME/.tiler/{images,sessions,cache}) is already sandboxed and
# needs no --*-dir flags. Mount targets must sit OUTSIDE the sessions
# tree and the reserved directories, so keep them under $HOME/work.
# s_dir and s2_dir are two views of one named session (dev); orph_dir
# is a plain mount whose record is deleted to make it an orphan;
# uncov_dir is never mounted and stands in for a path no session covers.
src_dir=$HOME/src/app
s_dir=$HOME/work/status
s2_dir=$HOME/work/status2
orph_dir=$HOME/work/orph
uncov_dir=$HOME/work/uncovered

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
			"$fusermount3_bin" -uz "$_m" >/dev/null 2>&1 ||
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
	test_grep -E "^no sessions recorded$" empty.out &&
	"$tiler_bin" status --json >emptyj.out 2>emptyj.err &&
	test_grep -F "\"current_session\": \"\"" emptyj.out &&
	test_grep -F "\"sessions\": []" emptyj.out &&
	test_grep -F "\"orphan_views\": []" emptyj.out
'

# A path no mounted view covers is an error, not an empty result, in both
# forms. Needs only the binary: nothing is mounted to query.
test_expect_success TILER 'status of an uncovered path is an error in text and json' '
	mkdir -p "$uncov_dir" &&
	test_expect_code 1 "$tiler_bin" status "$uncov_dir" >uncov.out 2>uncov.err &&
	test_grep "no session covers" uncov.err &&
	test_expect_code 1 "$tiler_bin" status "$uncov_dir" --json >uncovj.out 2>uncovj.err &&
	test_grep "no session covers" uncovj.err
'

# Build one image and mount it at two views under a named session. With
# TILER_SESSION_ID naming that session, the overview marks it current and
# annotates both live mounts healthy, in text and json.
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'status overview marks a healthy current session' '
	mkdir -p "$src_dir/sub/deep" &&
	echo "hello tiler" >"$src_dir/README.txt" &&
	"$tiler_bin" build --image tmp/status "$src_dir" >build.out 2>build.err &&
	test_grep -E "^full tmp/status [0-9a-f]+" build.out &&
	"$tiler_bin" session new dev >new.out 2>new.err &&
	test_grep -E "^created dev$" new.out &&
	mkdir -p "$s_dir" "$s2_dir" &&
	"$tiler_bin" mount --session dev tmp/status "$s_dir" >m1.out 2>m1.err &&
	test_grep -E "^mounted " m1.out &&
	"$tiler_bin" mount --session dev tmp/status "$s2_dir" >m2.out 2>m2.err &&
	test_grep -E "^mounted " m2.out &&
	TILER_SESSION_ID=dev "$tiler_bin" status >ov.out 2>ov.err &&
	test_grep -E "^dev active created=.* current$" ov.out &&
	test_grep -E "^  1 mount tmp/status .* mode=rw status=mounted health=healthy$" ov.out &&
	test_grep -E "^  2 mount tmp/status .* mode=rw status=mounted health=healthy$" ov.out &&
	TILER_SESSION_ID=dev "$tiler_bin" status --json >ovj.out 2>ovj.err &&
	test_grep -F "\"current_session\": \"dev\"" ovj.out &&
	test_grep -F "\"current\": true" ovj.out &&
	test "$(grep -cF "\"health\": \"healthy\"" ovj.out)" -eq 2
'

# The environment marker is carried verbatim and may match no record;
# that is not an error. No header is marked current, yet the json
# current_session still echoes the marker.
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'a current marker matching no record is not an error' '
	TILER_SESSION_ID=bogus "$tiler_bin" status >nc.out 2>nc.err &&
	test_grep -E "^dev active created=" nc.out &&
	test_grep ! -E " current$" nc.out &&
	TILER_SESSION_ID=bogus "$tiler_bin" status --json >ncj.out 2>ncj.err &&
	test_grep -F "\"current_session\": \"bogus\"" ncj.out &&
	test_grep -F "\"current\": false" ncj.out
'

# The path form resolves a deep path inside the merged view and the
# mountpoint itself to the covering session. Its action line carries no
# health mark (that lives only in the overview and the json), and a
# healthy view is not stale.
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'status of a path resolves the covering session' '
	"$tiler_bin" status "$s_dir/sub/deep/x.txt" >p1.out 2>p1.err &&
	test_grep -E "^dev active mountpoint=" p1.out &&
	test_grep -F "$s_dir" p1.out &&
	test_grep ! " stale" p1.out &&
	test_grep -E "^  1 mount tmp/status .* mode=rw status=mounted$" p1.out &&
	"$tiler_bin" status "$s_dir" >p2.out 2>p2.err &&
	test_grep -E "^dev active mountpoint=" p2.out &&
	test_grep -F "$s_dir" p2.out &&
	"$tiler_bin" status "$s_dir/sub/deep/x.txt" --json >p3.out 2>p3.err &&
	test_grep -F "\"stale\": false" p3.out &&
	test_grep -F "\"session\": \"dev\"" p3.out &&
	test_grep -F "\"health\": \"healthy\"" p3.out &&
	test_grep -F "$s_dir" p3.out
'

# SIGKILL the daemon behind the second view: it goes stale (any access
# returns ENOTCONN). The overview annotates that view stale while its
# peer stays healthy, the path form marks the header stale, and the json
# reports stale with a stale action health.
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN,KILL_VIEW 'status reports a killed view stale while its peer stays healthy' '
	kill_view "$s2_dir" &&
	sleep 1 &&
	! ls "$s2_dir" >/dev/null 2>&1 &&
	"$tiler_bin" status >ov6.out 2>ov6.err &&
	test_grep -E "^  1 mount tmp/status .* mode=rw status=mounted health=healthy$" ov6.out &&
	test_grep -E "^  2 mount tmp/status .* mode=rw status=mounted health=stale$" ov6.out &&
	"$tiler_bin" status "$s2_dir" >ps6.out 2>ps6.err &&
	test_grep -E " stale$" ps6.out &&
	test_grep -F "$s2_dir" ps6.out &&
	test_grep -E "^  2 mount tmp/status .* mode=rw status=mounted$" ps6.out &&
	"$tiler_bin" status "$s2_dir" --json >pj6.out 2>pj6.err &&
	test_grep -F "\"stale\": true" pj6.out &&
	test_grep -F "\"health\": \"stale\"" pj6.out
'

# close tears both views down (the killed one included) and flips the
# record to closed. The overview then shows the closed header with no
# expanded actions and no orphans, and the json carries an empty action
# list.
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'a closed session shows no live views or orphans' '
	"$tiler_bin" session close dev >cl7.out 2>cl7.err &&
	test_grep -E "^closed dev$" cl7.out &&
	"$tiler_bin" status >ov7.out 2>ov7.err &&
	test_grep -E "^dev closed created=" ov7.out &&
	test_grep ! "^  " ov7.out &&
	test_grep ! "^orphan " ov7.out &&
	"$tiler_bin" status --json >j7.out 2>j7.err &&
	test_grep -F "\"state\": \"closed\"" j7.out &&
	test_grep -F "\"actions\": []" j7.out &&
	test_grep -F "\"orphan_views\": []" j7.out
'

# A plain mount with no --session mints a generated record. Delete
# that record file out-of-band while the view stays live: the mount is
# now claimed by no active record, so the overview lists it as an orphan
# and the json carries a non-empty orphan_views naming the view point.
# Only session.json is removed — the session directory still holds live
# runtime mounts under runtime/m<seq>/, so rm -rf of the whole tree
# cannot succeed and is not the "lost record" signal names()/Load use.
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'status lists a live view whose record was lost as an orphan' '
	mkdir -p "$orph_dir" &&
	"$tiler_bin" mount tmp/status "$orph_dir" >m8.out 2>m8.err &&
	test_grep -E "^mounted " m8.out &&
	auto=$(sed -n "s/^new session created: //p" m8.out) &&
	test -n "$auto" &&
	rm -f "$HOME/.tiler/sessions/$auto/session.json" &&
	"$tiler_bin" status >ov8.out 2>ov8.err &&
	test_grep -E "^orphan " ov8.out &&
	test_grep -F "$orph_dir" ov8.out &&
	test_grep -E "^orphan .* fuse\.fuse-overlayfs " ov8.out &&
	"$tiler_bin" status --json >j8.out 2>j8.err &&
	test_grep -F "\"point\"" j8.out &&
	test_grep -F "$orph_dir" j8.out &&
	test_grep -F "\"fstype\": \"fuse.fuse-overlayfs\"" j8.out
'

test_done
