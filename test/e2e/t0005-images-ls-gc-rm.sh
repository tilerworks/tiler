#!/bin/sh
#
# tiler end-to-end test: images ls / gc / rm lifecycle.
#
# Companion to t0004-image-incr-cascade.sh. Where t0004 drives the
# incremental cascade and the full merge, this case exercises the
# library-management subcommands over a real, mounted image:
#
#   1. images list/show render the active image, its chain, and --json,
#
# It mirrors the listing, gc, and rm scenarios of the out-of-repo
# tiler-demo smoke harness (run_tests.sh S02, S13, S19), re-expressed in
# the vendored git-test-lib framework so `make e2e` runs it.
#
# A real mount needs a writable /dev/fuse plus the squashfs and overlay
# toolchain that `tiler doctor` treats as fail-level. FUSE_TOOLCHAIN from
# sharness.d/tiler.sh models exactly that, so the case skips cleanly -- rather
# than false-fails -- on a host without the toolchain. The gc/rm pins
# are created by real mounts, so the whole case is gated on it.
#
# Scope guard: this case asserts ls/gc/rm over the default single-file
# storage only. It does not cover split storage, cache, session
# subcommands, crash recovery, or the incremental cascade (t0004).

test_description='tiler images ls, gc, and rm manage the library lifecycle'

# Resolve paths against this script's location BEFORE sourcing the
# framework: test-lib.sh moves the working directory into a per-run
# trash directory and repoints HOME there.
tiler_case_dir=$(cd "$(dirname "$0")" && pwd)
TEST_LIB_DIRECTORY=$(cd "$tiler_case_dir/../git-test-lib" && pwd)
export TEST_LIB_DIRECTORY
TEST_DIRECTORY=$tiler_case_dir
export TEST_DIRECTORY

. "$TEST_LIB_DIRECTORY/test-lib.sh"

# tiler_bin, TILER, LINUX, and FUSE_TOOLCHAIN come from sharness.d/tiler.sh.

# HOME is the per-run trash directory, so tiler's default layout
# ($HOME/.tiler/{images,runtime,sessions,cache}) is already sandboxed and
# needs no --*-dir flags. Mount targets must sit OUTSIDE the runtime tree
# and the reserved directories, so keep them under $HOME/work. work_dir
# carries alice/app through the gc lifecycle; work2_dir carries life/app
# for the rm-referenced refusal.
src_dir=$HOME/src/app
work_dir=$HOME/work/app
work2_dir=$HOME/work/life

# Guarantee every FUSE mount is torn down before the framework removes
# the trash directory, even if an assertion fails or the run is
# interrupted. `tiler umount` is idempotent; fusermount3 -uz is the
# fallback for a view whose daemon already died.
if test -n "$tiler_bin"
then
	test_atexit '
		for _m in "$work_dir" "$work2_dir"
		do
			"$tiler_bin" umount "$_m" >/dev/null 2>&1 ||
			"$fusermount3_bin" -uz "$_m" >/dev/null 2>&1 ||
			true
		done
	'
fi

# git-test-lib eval's each body in the current shell, so id1/id2 captured
# below persist across the later test_expect_success blocks (and the .out
# files persist in the trash working dir too). id1 and id2 are two full

# One active image with a single increment exercises every ls form: the
# all-images line, the per-name chain expansion, and the --json document.
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'images list and show render the active image, its chain, and json' '
	mkdir -p "$src_dir" &&
	echo "hello tiler" >"$src_dir/README.txt" &&
	"$tiler_bin" build --image alice/app "$src_dir" >build.out 2>build.err &&
	test_grep -E "^full alice/app [0-9a-f]+" build.out &&
	id1=$(cut -d" " -f3 build.out) &&
	test -n "$id1" &&
	mkdir -p "$work_dir" &&
	"$tiler_bin" mount alice/app "$work_dir" >mount.out 2>mount.err &&
	test_grep -E "^mounted " mount.out &&
	echo edit >"$work_dir/NEW.txt" &&
	( cd "$work_dir" && "$tiler_bin" build --incremental ) >incr.out 2>incr.err &&
	test_grep -E "^incr alice/app [0-9a-f]+" incr.out &&
	"$tiler_bin" images ls >lsall.out 2>lsall.err &&
	test_grep "alice/app" lsall.out &&
	"$tiler_bin" images show alice/app >ls.out 2>ls.err &&
	test_grep -E "^  parent +$id1$" ls.out &&
	test_grep -E "^    $id1$" ls.out &&
	"$tiler_bin" images ls --json >lsjson.out 2>lsjson.err &&
	test_grep "\"images\"" lsjson.out
'

# Mounting without --session creates a generated session that pins the
# image. While that view is live, rm must be refused with a non-zero
# exit naming the pinning session. Umounting releases the pin, after
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'rm is refused while an active session references it' '
	"$tiler_bin" build --image life/app "$src_dir" >build5.out 2>build5.err &&
	test_grep -E "^full life/app [0-9a-f]+" build5.out &&
	mkdir -p "$work2_dir" &&
	"$tiler_bin" mount life/app "$work2_dir" >mount5.out 2>mount5.err &&
	test_grep -E "^mounted " mount5.out &&
	pin=$(sed -n "s/^new session created: //p" mount5.out) &&
	test -n "$pin" &&
	test_must_fail "$tiler_bin" image rm life/app >rmref.out 2>rmref.err &&
	test_grep "referenced by sessions" rmref.err &&
	"$tiler_bin" umount "$work2_dir" &&
	"$tiler_bin" session close "$pin" && "$tiler_bin" session remove "$pin" &&
	"$tiler_bin" image rm life/app >rm3.out 2>rm3.err
'

test_done
