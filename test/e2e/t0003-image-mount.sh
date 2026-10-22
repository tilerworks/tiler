#!/bin/sh
#
# tiler end-to-end test: image create, mount, and repeat mount.
#
# Companion to t0000-doctor.sh. Where the config cases (t0001/t0002)
# stay mount-free, this one drives the real storage and mount path end
# to end:
#
#   1. build a full image from a plain source tree,
#   2. mount it read-write and write into the upper layer,
#   3. mount the same view again and confirm it is idempotent,
#   4. build a second image and confirm it gets a distinct id,
#   5. mount the second image at its own target,
#   6. refuse a different image mounted onto a live view (conflict),
#   7. umount both views.
#
# It mirrors the build/mount/remount and different-identity-conflict
# scenarios of the out-of-repo tiler-demo smoke harness (run_tests.sh
# and verify_gaps.sh G06), re-expressed in the vendored git-test-lib
# framework so `make e2e` runs it.
#
# A real mount needs a writable /dev/fuse plus the squashfs and overlay
# toolchain that `tiler doctor` treats as fail-level. FUSE_TOOLCHAIN from
# sharness.d/tiler.sh models exactly that, so the case skips cleanly --
# rather than false-fails -- on a host without the toolchain.
#
# Scope guard: this case asserts build + mount + idempotent remount +
# distinct-image conflict + umount only. It does not cover sessions,
# crash recovery, incremental builds, split storage, or
# read-only/subdir/cache mount variants.

test_description='tiler builds, mounts, remounts, and refuses a conflicting image'

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
# needs no --*-dir flags. The mount target must sit OUTSIDE the runtime
# tree and the reserved directories, so keep it under $HOME/work.
src_dir=$HOME/src/app
work_dir=$HOME/work/app
src2_dir=$HOME/src/tools
work2_dir=$HOME/work/tools
image=$HOME/.tiler/images/alice/app/image.sqsh

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

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'build a full image from a source tree' '
	mkdir -p "$src_dir/docs" &&
	echo "hello tiler" >"$src_dir/README.txt" &&
	echo "# Guide" >"$src_dir/docs/guide.md" &&
	"$tiler_bin" build --image alice/app "$src_dir" >build.out 2>build.err &&
	test_grep -E "^full alice/app [0-9a-f]+" build.out &&
	test_path_is_file "$image" &&
	test_path_is_missing "${image%.sqsh}.dmg"
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'mount the image read-write with an upper layer' '
	mkdir -p "$work_dir" &&
	"$tiler_bin" mount alice/app "$work_dir" >mount.out 2>mount.err &&
	test_grep -E "^mounted " mount.out &&
	test_grep "hello tiler" "$work_dir/README.txt" &&
	test_path_is_file "$work_dir/docs/guide.md" &&
	echo edited >"$work_dir/NEW.txt" &&
	test_path_is_file "$work_dir/NEW.txt"
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'repeat mount of the same view is idempotent' '
	"$tiler_bin" mount alice/app "$work_dir" >remount.out 2>remount.err &&
	test_grep -E "^already mounted " remount.out
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'a second image builds with a distinct id' '
	mkdir -p "$src2_dir" &&
	echo "#!/bin/sh" >"$src2_dir/run.sh" &&
	"$tiler_bin" build --image bob/tools "$src2_dir" >build2.out 2>build2.err &&
	test_grep -E "^full bob/tools [0-9a-f]+" build2.out &&
	id1=$(cut -d" " -f3 build.out) &&
	id2=$(cut -d" " -f3 build2.out) &&
	test -n "$id1" && test -n "$id2" &&
	test "$id1" != "$id2"
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'the second image mounts at its own target' '
	mkdir -p "$work2_dir" &&
	"$tiler_bin" mount bob/tools "$work2_dir" >mount2.out 2>mount2.err &&
	test_grep -E "^mounted " mount2.out &&
	test_path_is_file "$work2_dir/run.sh" &&
	test_path_is_missing "$work2_dir/README.txt"
'

# work_dir is still live serving alice/app. Mounting a different image
# there must be refused as a conflict, not silently restacked: the exit
# is non-zero, the error names the refusal, and the live view still
# serves alice/app (README.txt present, run.sh absent).
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'a different image on a live view is refused' '
	test_must_fail "$tiler_bin" mount bob/tools "$work_dir" >conflict.out 2>conflict.err &&
	test_grep "already mounts a different view" conflict.err &&
	test_grep "refusing to restack it silently" conflict.err &&
	test_grep "hello tiler" "$work_dir/README.txt" &&
	test_path_is_missing "$work_dir/run.sh"
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'umount tears both views down' '
	"$tiler_bin" umount "$work_dir" &&
	"$tiler_bin" umount "$work2_dir"
'

test_done
