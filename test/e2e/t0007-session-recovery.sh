#!/bin/sh
#
# tiler end-to-end test: session crash recovery via SIGKILL + open.
#
# Companion to t0006-session-lifecycle.sh. Where t0006 walks the named
# lifecycle in the foreground, this case simulates a daemon crash and
# proves `session open` rebuilds the view from the write-ahead record:
#
#   1. a SIGKILLed view leaves the record intact (mounts unchanged),
#   2. session open replays the stale mount and the writable upper
#      layer survives, so a file written before the crash is still there,
#   3. a second session open is a pure no-op over the healthy view,
#   4. a closed session revives through open and remounts its view.
#
# It mirrors the restart-recovery scenario of the out-of-repo tiler-e2e
# s05 step (SIGKILL the FUSE daemons, then one idempotent session open),
# re-expressed in the vendored git-test-lib framework so `make e2e`
# runs it.
#
# A real mount needs a writable /dev/fuse plus the squashfs and overlay
# toolchain that `tiler doctor` treats as fail-level, and the crash needs
# pgrep to find the daemon. FUSE_TOOLCHAIN and KILL_VIEW from
# sharness.d/tiler.sh model exactly that, so the case skips cleanly --
# rather than false-fails -- on a host without the toolchain.
#
# Scope guard: this case asserts crash recovery of one named session
# over the default single-file storage only. It does not cover the named
# lifecycle's error branches (t0006), generated sessions (t0008),
# git-backed images, split storage, or multi-view sessions.

test_description='tiler session open recovers a view after a daemon crash'

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
# ($HOME/.tiler/{images,runtime,sessions,cache}) is already sandboxed and
# needs no --*-dir flags. Mount targets must sit OUTSIDE the runtime tree
# and the reserved directories, so keep them under $HOME/work. rec_dir is
# the single writable view whose daemon this case kills and recovers.
src_dir=$HOME/src/app
rec_dir=$HOME/work/rec

# Guarantee the view is torn down before the framework removes the trash
# directory, even if an assertion fails, the daemon was killed, or the
# run is interrupted. `tiler umount` is idempotent; fusermount3 -uz is
# the fallback for a view whose daemon already died.
if test -n "$tiler_bin"
then
	test_atexit '
		for _m in "$rec_dir"
		do
			"$tiler_bin" umount "$_m" >/dev/null 2>&1 ||
			"$fusermount3_bin" -uz "$_m" >/dev/null 2>&1 ||
			true
		done
	'
fi

# git-test-lib eval's each body in the current shell, so the `rec` record
# and the mounted view persist across the four blocks below: crash it,
# recover it, re-open it, then close and revive it.

# Build a writable view under a named session, write a file into its
# upper layer, then SIGKILL the daemon. The view goes stale (any access
# returns ENOTCONN, so `ls` fails) but the write-ahead record survives
# untouched and still reports its pre-crash mounts= count.
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN,KILL_VIEW 'the record survives a daemon crash' '
	mkdir -p "$src_dir" &&
	echo "hello tiler" >"$src_dir/README.txt" &&
	"$tiler_bin" build --image alice/app "$src_dir" >build.out 2>build.err &&
	test_grep -E "^full alice/app [0-9a-f]+" build.out &&
	"$tiler_bin" session new rec >new.out 2>new.err &&
	test_grep -E "^created rec$" new.out &&
	mkdir -p "$rec_dir" &&
	"$tiler_bin" mount --session rec alice/app "$rec_dir" >m1.out 2>m1.err &&
	test_grep -E "^mounted " m1.out &&
	test_grep hello "$rec_dir/README.txt" &&
	echo survived >"$rec_dir/KEEP.txt" &&
	kill_view "$rec_dir" &&
	sleep 1 &&
	! ls "$rec_dir" >/dev/null 2>&1 &&
	"$tiler_bin" session list >list1.out 2>list1.err &&
	test_grep -E "^rec[[:space:]]+active[[:space:]]+1[[:space:]]" list1.out
'

# session open finds the stale view unhealthy, cleans and remounts it,
# and writes the action back to mounted. The upper layer is keyed by the
# mountpoint inode, so the remount reuses it and the file written before
# the crash is still there.
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN,KILL_VIEW 'session open replays the view and the upper survives' '
	"$tiler_bin" session open rec >open1.out 2>open1.err &&
	test_grep -E "^opened rec$" open1.out &&
	test_grep hello "$rec_dir/README.txt" &&
	test_path_is_file "$rec_dir/KEEP.txt" &&
	test_grep survived "$rec_dir/KEEP.txt" &&
	"$tiler_bin" session list >list2.out 2>list2.err &&
	test_grep -E "^rec[[:space:]]+active[[:space:]]+1[[:space:]]" list2.out
'

# A second open over the now-healthy view is a pure no-op: the health
# check skips the remount, so the confirmation still prints and the
# upper-layer file is untouched.
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN,KILL_VIEW 'a second session open is idempotent' '
	"$tiler_bin" session open rec >open2.out 2>open2.err &&
	test_grep -E "^opened rec$" open2.out &&
	test_grep hello "$rec_dir/README.txt" &&
	test_path_is_file "$rec_dir/KEEP.txt"
'

# close tears the healthy view down and flips the record to closed; open
# then revives it -- reopen sets the state back to active and the replay
# remounts the view, reusing the same upper so the file survives again.
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN,KILL_VIEW 'a closed session revives through open' '
	"$tiler_bin" session close rec >close.out 2>close.err &&
	test_grep -E "^closed rec$" close.out &&
	"$tiler_bin" session list >list4a.out 2>list4a.err &&
	test_grep -E "^rec[[:space:]]+closed[[:space:]]+0[[:space:]]" list4a.out &&
	test_path_is_missing "$rec_dir/README.txt" &&
	"$tiler_bin" session open rec >open3.out 2>open3.err &&
	test_grep -E "^opened rec$" open3.out &&
	"$tiler_bin" session list >list4b.out 2>list4b.err &&
	test_grep -E "^rec[[:space:]]+active[[:space:]]+1[[:space:]]" list4b.out &&
	test_grep hello "$rec_dir/README.txt" &&
	test_path_is_file "$rec_dir/KEEP.txt"
'

test_done
