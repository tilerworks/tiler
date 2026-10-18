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
#   4. umount the view.
#
# It mirrors the build/mount/remount scenarios of the out-of-repo
# tiler-demo smoke harness, re-expressed in the vendored git-test-lib
# framework so `make e2e` runs it.
#
# A real mount needs a writable /dev/fuse plus the squashfs and overlay
# toolchain that `tiler doctor` treats as fail-level. The FUSE_TOOLCHAIN
# prereq below models exactly that, so the case skips cleanly -- rather
# than false-fails -- on a host without the toolchain.
#
# Scope guard: this case asserts build + mount + idempotent remount +
# umount only. It does not cover sessions, crash recovery, incremental
# builds, split storage, or read-only/subdir/cache mount variants.

test_description='tiler builds an image, mounts it, and remounts idempotently'

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

# HOME is the per-run trash directory, so tiler's default layout
# ($HOME/.tiler/{images,runtime,sessions,cache}) is already sandboxed and
# needs no --*-dir flags. The mount target must sit OUTSIDE the runtime
# tree and the reserved directories, so keep it under $HOME/work.
src_dir=$HOME/src/app
work_dir=$HOME/work/app

# Guarantee the FUSE mount is torn down before the framework removes the
# trash directory, even if an assertion fails or the run is interrupted.
# `tiler umount` is idempotent; fusermount3 is the fallback for a view
# whose daemon already died.
if test -n "$tiler_bin"
then
	test_atexit '
		"$tiler_bin" umount "$work_dir" >/dev/null 2>&1 ||
		fusermount3 -u "$work_dir" >/dev/null 2>&1 ||
		fusermount3 -uz "$work_dir" >/dev/null 2>&1 ||
		true
	'
fi

test_expect_success TILER,FUSE_TOOLCHAIN 'build a full image from a source tree' '
	mkdir -p "$src_dir/docs" &&
	echo "hello tiler" >"$src_dir/README.txt" &&
	echo "# Guide" >"$src_dir/docs/guide.md" &&
	"$tiler_bin" build --image alice/app "$src_dir" >build.out 2>build.err &&
	grep -Eq "^full alice/app [0-9a-f]+" build.out &&
	find "$HOME/.tiler/images" -name "*.sqsh" | grep -q .
'

test_expect_success TILER,FUSE_TOOLCHAIN 'mount the image read-write with an upper layer' '
	mkdir -p "$work_dir" &&
	"$tiler_bin" mount alice/app "$work_dir" >mount.out 2>mount.err &&
	grep -Eq "^mounted " mount.out &&
	grep -q "hello tiler" "$work_dir/README.txt" &&
	test -f "$work_dir/docs/guide.md" &&
	echo edited >"$work_dir/NEW.txt" &&
	test -f "$work_dir/NEW.txt"
'

test_expect_success TILER,FUSE_TOOLCHAIN 'repeat mount of the same view is idempotent' '
	"$tiler_bin" mount alice/app "$work_dir" >remount.out 2>remount.err &&
	grep -Eq "^already mounted " remount.out
'

test_expect_success TILER,FUSE_TOOLCHAIN 'umount tears the view down' '
	"$tiler_bin" umount "$work_dir"
'

test_done
