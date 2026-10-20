#!/bin/sh
#
# tiler end-to-end test: non-default squashfs compression.
#
# Companion to t0003-image-mount.sh (default zstd full + writable
# mount) and t0013-mount-file-path.sh (a bare squashfs path). This
# case drives compression selection:
#
#   1. a full build with --compression gzip (not the zstd default)
#      records squashfs/gzip and keeps the active file image.sqsh,
#   2. a writable mount of that image, then an incremental build
#      with no format or compression flags, inherits squashfs/gzip,
#   3. a fresh remount serves the file written into the upper,
#   4. a squashfs packed outside the library with mksquashfs -comp xz
#      mounts and accepts an upper-layer write.
#
# A real mount needs a writable /dev/fuse plus the squashfs and overlay
# toolchain that `tiler doctor` treats as fail-level. The FUSE_TOOLCHAIN
# prereq below models exactly that, so the case skips cleanly -- rather
# than false-fails -- on a host without the toolchain.

test_description='tiler builds and mounts non-default squashfs compression'

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
# ($HOME/.tiler/{images,sessions,cache}) is already sandboxed and
# needs no --*-dir flags. Mount targets must sit OUTSIDE the runtime
# tree and the reserved directories, so keep them under $HOME/work.
#
# work_dir hosts the writable view the increment is built in.
# verify_dir is a second, fresh mount point: a remount of work_dir
# reuses that point's upper, so only a brand-new point starts empty
# and proves extra.txt came from the incr layer.
# ext_dir mounts a squashfs that tiler did not pack.
src_dir=$HOME/src/gzip
work_dir=$HOME/work/gzip
verify_dir=$HOME/work/verify
img_dir=$HOME/.tiler/images/alice/gzip
meta_file=$img_dir/meta.yaml
ext_src=$HOME/src/vendor
nas_dir=$HOME/nas
ext_img=$nas_dir/vendor.sqsh
ext_dir=$HOME/work/vendor

if test -n "$tiler_bin"
then
	test_atexit '
		for _m in "$work_dir" "$verify_dir" "$ext_dir"
		do
			"$tiler_bin" umount "$_m" >/dev/null 2>&1 ||
			fusermount3 -uz "$_m" >/dev/null 2>&1 ||
			true
		done
	'
fi

# gzip is on the squashfs whitelist and is not the format default
# (zstd). The active payload stays image.sqsh.
test_expect_success TILER,FUSE_TOOLCHAIN 'gzip full build records squashfs/gzip' '
	mkdir -p "$src_dir" &&
	echo "hello gzip" >"$src_dir/README.txt" &&
	"$tiler_bin" build --format squashfs --compression gzip --image alice/gzip "$src_dir" >build.out 2>build.err &&
	grep -Eq "^full alice/gzip [0-9a-f]+" build.out &&
	id1=$(cut -d" " -f3 build.out) &&
	test -n "$id1" &&
	test -f "$img_dir/image.sqsh" &&
	test ! -e "$img_dir/blocks" &&
	grep -Eq "^id: \"?$id1\"?$" "$meta_file" &&
	grep -q "^    type: squashfs$" "$meta_file" &&
	grep -q "^    compression: gzip$" "$meta_file" &&
	"$tiler_bin" images show alice/gzip >show.out 2>show.err &&
	grep -Eq "^  format +squashfs/gzip$" show.out
'

# The incremental build passes neither --format nor --compression, so
# the layer has to inherit the active squashfs/gzip pair. A conflicting
# flag would be a usage error; this path is the same-format append.
test_expect_success TILER,FUSE_TOOLCHAIN 'writable mount and same-format incr inherit gzip' '
	mkdir -p "$work_dir" &&
	"$tiler_bin" mount alice/gzip "$work_dir" >mount.out 2>mount.err &&
	grep -Eq "^mounted " mount.out &&
	grep -q "hello gzip" "$work_dir/README.txt" &&
	echo from-incr >"$work_dir/extra.txt" &&
	test -f "$work_dir/extra.txt" &&
	( cd "$work_dir" && "$tiler_bin" build --incremental ) >incr.out 2>incr.err &&
	grep -Eq "^incr alice/gzip [0-9a-f]+" incr.out &&
	layer=$(cut -d" " -f3 incr.out) &&
	test -n "$layer" &&
	test "$layer" != "$id1" &&
	test -f "$img_dir/image.sqsh" &&
	test -f "$img_dir/incr/$layer.sqsh" &&
	grep -q "^    type: squashfs$" "$meta_file" &&
	grep -q "^    compression: gzip$" "$meta_file" &&
	"$tiler_bin" images show alice/gzip >show-incr.out 2>show-incr.err &&
	grep -Eq "^  format +squashfs/gzip$" show-incr.out &&
	grep -Eq "^  chain +1$" show-incr.out
'

test_expect_success TILER,FUSE_TOOLCHAIN 'a fresh remount serves the gzip incr' '
	"$tiler_bin" umount "$work_dir" >umount.out 2>umount.err &&
	grep -Eq "^unmounted " umount.out &&
	mkdir -p "$verify_dir" &&
	"$tiler_bin" mount alice/gzip "$verify_dir" >remount.out 2>remount.err &&
	grep -Eq "^mounted " remount.out &&
	grep -q "hello gzip" "$verify_dir/README.txt" &&
	grep -q from-incr "$verify_dir/extra.txt" &&
	"$tiler_bin" umount "$verify_dir" >umount2.out 2>umount2.err &&
	grep -Eq "^unmounted " umount2.out
'

# Packed with mksquashfs directly, so the payload never enters the
# library and never grows a meta.yaml. xz is another non-default
# compressor; squashfuse_ll mounts the file by magic.
test_expect_success TILER,FUSE_TOOLCHAIN 'an external xz squashfs mounts read-write' '
	mkdir -p "$ext_src" "$nas_dir" "$ext_dir" &&
	echo "hello xz" >"$ext_src/README.txt" &&
	mksquashfs "$ext_src" "$ext_img" -comp xz -noappend >/dev/null &&
	test -f "$ext_img" &&
	test ! -e "$nas_dir/meta.yaml" &&
	"$tiler_bin" mount "$ext_img" "$ext_dir" >ext.out 2>ext.err &&
	grep -Eq "^mounted " ext.out &&
	grep -q "hello xz" "$ext_dir/README.txt" &&
	echo upper >"$ext_dir/UPPER.txt" &&
	test -f "$ext_dir/UPPER.txt" &&
	"$tiler_bin" umount "$ext_dir" >ext-umount.out 2>ext-umount.err &&
	grep -Eq "^unmounted " ext-umount.out
'

test_done
