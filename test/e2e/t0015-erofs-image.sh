#!/bin/sh
#
# tiler end-to-end test: erofs images and a single erofs toolchain.
#
# Companion to t0014-squashfs-compression.sh (squashfs gzip / xz) and
# t0012-build-split-size.sh (blocks). This case drives the erofs
# format:
#
#   1. --format erofs --compression xz is a usage error and publishes
#      nothing,
#   2. a project image_format: erofs, with no --format flag, packs a
#      full image as image.sqsh. The compression is zstd when
#      mkfs.erofs provides it, otherwise lz4, then gzip,
#   3. a writable mount and an incremental build with no format flags
#      inherit that pair; a fresh remount serves the upper file,
#   4. the same project config with --split 64k publishes blocks and
#      still mounts,
#   5. --subdir exposes one image directory as the mount root,
#   6. doctor exits 0 when PATH offers only the erofs stack (the
#      missing squashfs stack warns and does not fail the run).
#
# A real erofs build needs /dev/fuse, mkfs.erofs, erofsfuse, and the
# shared overlay tools. The EROFS_TOOLCHAIN prereq below models exactly
# that, so the case skips cleanly -- rather than false-fails -- on a
# host without the erofs stack, including a squashfs-only host.

test_description='tiler builds, mounts, and checks an erofs image'

# Resolve paths against this script's location BEFORE sourcing the
# framework: test-lib.sh moves the working directory into a per-run
# trash directory and repoints HOME there.
tiler_case_dir=$(cd "$(dirname "$0")" && pwd)
TEST_LIB_DIRECTORY=$(cd "$tiler_case_dir/../git-test-lib" && pwd)
export TEST_LIB_DIRECTORY
TEST_DIRECTORY=$tiler_case_dir
export TEST_DIRECTORY

. "$TEST_LIB_DIRECTORY/test-lib.sh"

# An omitted erofs compression prefers zstd, then lz4, then gzip,
# matching the compressors this mkfs.erofs was built with.
erofs_comp=zstd
erofs_help=$(mkfs.erofs --help 2>&1 || true)
erofs_has() {
	printf '%s\n' "$erofs_help" | tr ',:' '  ' |
		grep -Eq "(^|[[:space:]])$1([[:space:]]|$)"
}
if erofs_has zstd; then
	erofs_comp=zstd
elif erofs_has lz4; then
	erofs_comp=lz4
elif erofs_has deflate || erofs_has libdeflate; then
	erofs_comp=gzip
fi

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

# erofs packing and mounting need the erofs stack plus the shared FUSE
# tools doctor still treats as fail-level. git is included because the
# single-stack doctor assertion expects that shared probe to pass.
# A squashfs-only host leaves this prereq unmet and the case skips.
test_lazy_prereq EROFS_TOOLCHAIN '
	test -e /dev/fuse &&
	command -v mkfs.erofs >/dev/null 2>&1 &&
	command -v erofsfuse >/dev/null 2>&1 &&
	command -v fuse-overlayfs >/dev/null 2>&1 &&
	command -v fusermount3 >/dev/null 2>&1 &&
	command -v git >/dev/null 2>&1
'

# HOME is the per-run trash directory, so tiler's default layout
# ($HOME/.tiler/{images,sessions,cache}) is already sandboxed.
# proj/ carries image_format: erofs. Mount targets stay under
# $HOME/work, outside that project tree, so an incremental build
# there cannot pick the format up from a nearer project file; it
# has to inherit the active image.
proj=$HOME/proj
src_dir=$proj/src
work_dir=$HOME/work/erofs
verify_dir=$HOME/work/verify
sub_dir=$HOME/work/sub
img_dir=$HOME/.tiler/images/alice/erofs
meta_file=$img_dir/meta.yaml
blk_src=$HOME/src/blocks
blk_dir=$HOME/.tiler/images/alice/blocks
blk_meta=$blk_dir/meta.yaml
blk_work=$HOME/work/blocks

if test -n "$tiler_bin"
then
	test_atexit '
		for _m in "$work_dir" "$verify_dir" "$sub_dir" "$blk_work"
		do
			"$tiler_bin" umount "$_m" >/dev/null 2>&1 ||
			fusermount3 -uz "$_m" >/dev/null 2>&1 ||
			true
		done
	'
fi

# xz is a squashfs compressor and is rejected for erofs before any
# image is published. The check is a usage error (exit 2).
test_expect_success TILER,EROFS_TOOLCHAIN 'erofs with xz is a usage error' '
	mkdir -p "$HOME/src/bad" &&
	echo nope >"$HOME/src/bad/README.txt" &&
	status=0 &&
	{ "$tiler_bin" build --format erofs --compression xz --image alice/bad "$HOME/src/bad" >bad.out 2>bad.err || status=$?; } &&
	test "$status" = 2 &&
	grep -F "erofs compression \"xz\" is not allowed" bad.err &&
	test ! -e "$HOME/.tiler/images/alice/bad"
'

# The project file sets the format. The build omits --format and
# --compression, so the image is erofs at the format default (zstd).
# The active file name stays image.sqsh.
test_expect_success TILER,EROFS_TOOLCHAIN 'project image_format erofs packs a full image' '
	mkdir -p "$src_dir/sub" &&
	printf "%s\n" "image_format: erofs" >"$proj/.tiler.yaml" &&
	echo "hello erofs" >"$src_dir/README.txt" &&
	echo inner >"$src_dir/sub/inner.txt" &&
	(
		cd "$proj" &&
		"$tiler_bin" build --image alice/erofs src
	) >build.out 2>build.err &&
	grep -Eq "^full alice/erofs [0-9a-f]+" build.out &&
	id1=$(cut -d" " -f3 build.out) &&
	test -n "$id1" &&
	test -f "$img_dir/image.sqsh" &&
	test ! -e "$img_dir/blocks" &&
	grep -Eq "^id: \"?$id1\"?$" "$meta_file" &&
	grep -q "^    type: erofs$" "$meta_file" &&
	grep -q "^    compression: $erofs_comp$" "$meta_file" &&
	"$tiler_bin" images show alice/erofs >show.out 2>show.err &&
	grep -Eq "^  format +erofs/$erofs_comp$" show.out &&
	grep -Eq "^  form +single$" show.out
'

test_expect_success TILER,EROFS_TOOLCHAIN 'writable mount and incr inherit the erofs compression' '
	mkdir -p "$work_dir" &&
	"$tiler_bin" mount alice/erofs "$work_dir" >mount.out 2>mount.err &&
	grep -Eq "^mounted " mount.out &&
	grep -q "hello erofs" "$work_dir/README.txt" &&
	test -f "$work_dir/sub/inner.txt" &&
	echo from-incr >"$work_dir/extra.txt" &&
	( cd "$work_dir" && "$tiler_bin" build --incremental ) >incr.out 2>incr.err &&
	grep -Eq "^incr alice/erofs [0-9a-f]+" incr.out &&
	layer=$(cut -d" " -f3 incr.out) &&
	test -n "$layer" &&
	test "$layer" != "$id1" &&
	test -f "$img_dir/image.sqsh" &&
	test -f "$img_dir/incr/$layer.sqsh" &&
	grep -q "^    type: erofs$" "$meta_file" &&
	grep -q "^    compression: $erofs_comp$" "$meta_file" &&
	"$tiler_bin" images show alice/erofs >show-incr.out 2>show-incr.err &&
	grep -Eq "^  format +erofs/$erofs_comp$" show-incr.out &&
	grep -Eq "^  chain +1$" show-incr.out
'

test_expect_success TILER,EROFS_TOOLCHAIN 'a fresh remount serves the erofs incr' '
	"$tiler_bin" umount "$work_dir" >umount.out 2>umount.err &&
	grep -Eq "^unmounted " umount.out &&
	mkdir -p "$verify_dir" &&
	"$tiler_bin" mount alice/erofs "$verify_dir" >remount.out 2>remount.err &&
	grep -Eq "^mounted " remount.out &&
	grep -q "hello erofs" "$verify_dir/README.txt" &&
	grep -q from-incr "$verify_dir/extra.txt" &&
	"$tiler_bin" umount "$verify_dir" >umount2.out 2>umount2.err &&
	grep -Eq "^unmounted " umount2.out
'

# 300 KiB of incompressible bytes so a 64k split spans more than one
# block. The project config still selects erofs; --split does not
# change the container. The payload lives in blocks/<id>/, not
# image.sqsh.
test_expect_success TILER,EROFS_TOOLCHAIN 'project image_format erofs --split publishes blocks' '
	mkdir -p "$blk_src" &&
	echo "hello blocks" >"$blk_src/README.txt" &&
	dd if=/dev/urandom of="$blk_src/bulk.bin" bs=1024 count=300 status=none &&
	(
		cd "$proj" &&
		"$tiler_bin" build --split 64k --image alice/blocks "$blk_src"
	) >blk.out 2>blk.err &&
	grep -Eq "^full alice/blocks [0-9a-f]+" blk.out &&
	blk_id=$(cut -d" " -f3 blk.out) &&
	test -n "$blk_id" &&
	test -d "$blk_dir/blocks/$blk_id" &&
	set -- "$blk_dir/blocks/$blk_id"/blk.[0-9][0-9][0-9] &&
	test $# -gt 1 &&
	test -f "$blk_dir/blocks/$blk_id/blk.000" &&
	test ! -e "$blk_dir/image.sqsh" &&
	grep -q "^    type: erofs$" "$blk_meta" &&
	grep -q "^    compression: $erofs_comp$" "$blk_meta" &&
	"$tiler_bin" images show alice/blocks >show-blk.out 2>show-blk.err &&
	grep -Eq "^  format +erofs/$erofs_comp$" show-blk.out &&
	grep -Eq "^  form +blocks$" show-blk.out &&
	mkdir -p "$blk_work" &&
	"$tiler_bin" mount alice/blocks "$blk_work" >blk-mount.out 2>blk-mount.err &&
	grep -Eq "^mounted " blk-mount.out &&
	grep -q "hello blocks" "$blk_work/README.txt" &&
	test -f "$blk_work/bulk.bin" &&
	"$tiler_bin" umount "$blk_work" >blk-umount.out 2>blk-umount.err &&
	grep -Eq "^unmounted " blk-umount.out
'

# --subdir makes sub/ the mount root. The image-root README must not
# leak through, and a read-only view rejects an upper write.
test_expect_success TILER,EROFS_TOOLCHAIN 'subdir mount exposes only that directory' '
	mkdir -p "$sub_dir" &&
	"$tiler_bin" mount --read-only --subdir sub alice/erofs "$sub_dir" >sub.out 2>sub.err &&
	grep -Eq "^mounted " sub.out &&
	grep -q inner "$sub_dir/inner.txt" &&
	test ! -e "$sub_dir/README.txt" &&
	status=0 &&
	{ echo nope >"$sub_dir/nope.txt" || status=$?; } &&
	test "$status" -ne 0 &&
	test ! -e "$sub_dir/nope.txt" &&
	"$tiler_bin" umount "$sub_dir" >sub-umount.out 2>sub-umount.err &&
	grep -Eq "^unmounted " sub-umount.out
'

# Hide the squashfs stack. One complete erofs stack is enough for
# doctor to exit 0: image_toolchain warns, and squashfuse_subdir
# warns because squashfuse_ll is not on this PATH.
test_expect_success TILER,EROFS_TOOLCHAIN 'doctor passes with only the erofs stack' '
	only=$HOME/erofs-only &&
	mkdir -p "$only" &&
	ln -s "$(command -v mkfs.erofs)" "$only/mkfs.erofs" &&
	ln -s "$(command -v erofsfuse)" "$only/erofsfuse" &&
	ln -s "$(command -v fuse-overlayfs)" "$only/fuse-overlayfs" &&
	ln -s "$(command -v fusermount3)" "$only/fusermount3" &&
	ln -s "$(command -v git)" "$only/git" &&
	PATH="$only" "$tiler_bin" doctor >doctor-stack.out 2>doctor-stack.err &&
	grep -Eq "^  image_toolchain +warn " doctor-stack.out &&
	grep -q "erofs ready" doctor-stack.out &&
	grep -q "squashfs missing" doctor-stack.out &&
	grep -Eq "^  squashfuse_subdir +warn " doctor-stack.out &&
	! grep -q "failing probes" doctor-stack.err
'

test_done
