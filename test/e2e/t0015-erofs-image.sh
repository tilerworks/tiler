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
#      append squashfs/zstd; a fresh remount serves the mixed chain,
#   4. the same project config with --split 64k publishes blocks and
#      still mounts,
#   5. --subdir exposes one image directory as the mount root,
#   6. doctor exits 0 when PATH offers only the erofs stack (the
#      missing squashfs stack warns and does not fail the run).
#
# A real erofs build needs /dev/fuse, mkfs.erofs, erofsfuse, and the
# shared overlay tools. EROFS_TOOLCHAIN from sharness.d/tiler.sh models
# exactly that, so the case skips cleanly -- rather than false-fails --
# on a host without the erofs stack, including a squashfs-only host.

test_description='tiler builds, mounts, and checks an erofs image'

. "$(dirname "$0")/lib-tiler.sh"

# tiler_bin, TILER, LINUX, and EROFS_TOOLCHAIN come from sharness.d/tiler.sh.

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
			"$fusermount3_bin" -uz "$_m" >/dev/null 2>&1 ||
			true
		done
	'
fi

# xz is a squashfs compressor and is rejected for erofs before any
# image is published. The check is a usage error (exit 2), but build
# checks for a complete toolchain before validating compression.
test_expect_success TILER,LINUX,EROFS_TOOLCHAIN 'erofs with xz is a usage error' '
	mkdir -p "$HOME/src/bad" &&
	echo nope >"$HOME/src/bad/README.txt" &&
	test_expect_code 2 "$tiler_bin" build --format erofs --compression xz --image alice/bad "$HOME/src/bad" >bad.out 2>bad.err &&
	test_grep -F "erofs compression \"xz\" is not allowed" bad.err &&
	test_path_is_missing "$HOME/.tiler/images/alice/bad"
'

# The project file sets the format. The build omits --format and
# --compression, so the image is erofs at the format default (zstd).
# The active file name stays image.sqsh.
test_expect_success TILER,LINUX,EROFS_TOOLCHAIN 'project image_format erofs packs a full image' '
	# mkfs.erofs may return nonzero for --help; only its advertised
	# compressors are relevant here, after EROFS_TOOLCHAIN was checked.
	{ erofs_help=$(mkfs.erofs --help 2>&1) || :; } &&
	printf "%s\n" "$erofs_help" >erofs-help.raw &&
	tr ",:" "  " <erofs-help.raw >erofs-help &&
	if grep -E "(^|[[:space:]])zstd([[:space:]]|$)" erofs-help
	then
		erofs_comp=zstd
	elif grep -E "(^|[[:space:]])lz4([[:space:]]|$)" erofs-help
	then
		erofs_comp=lz4
	elif grep -E "(^|[[:space:]])(deflate|libdeflate)([[:space:]]|$)" erofs-help
	then
		erofs_comp=gzip
	else
		erofs_comp=zstd
	fi &&
	mkdir -p "$src_dir/sub" &&
	printf "%s\n" "image_format: erofs" >"$proj/.tiler.yaml" &&
	echo "hello erofs" >"$src_dir/README.txt" &&
	echo inner >"$src_dir/sub/inner.txt" &&
	(
		cd "$proj" &&
		"$tiler_bin" build --image alice/erofs src 2>"$HOME/build.err"
	) >build.out &&
	test_grep -E "^full alice/erofs [0-9a-f]+" build.out &&
	id1=$(cut -d" " -f3 build.out) &&
	test -n "$id1" &&
	test_path_is_file "$img_dir/_objects/$id1/image.sqsh" &&
	test_path_is_missing "$img_dir/_blocks" &&
	test_grep -E "^[[:space:]]*- id: $id1$" "$meta_file" &&
	test_grep -E "^[[:space:]]+type: erofs$" "$meta_file" &&
	test_grep -E "^[[:space:]]+compression: $erofs_comp$" "$meta_file" &&
	"$tiler_bin" images show alice/erofs >show.out 2>show.err &&
	test_grep -E "^  format +erofs/$erofs_comp$" show.out &&
	test_grep -E "^  payload +single$" show.out
'

test_expect_success TILER,LINUX,EROFS_TOOLCHAIN,SQUASHFS_PACK 'a default squashfs increment can follow an erofs full' '
	mkdir -p "$work_dir" &&
	"$tiler_bin" mount alice/erofs "$work_dir" >mount.out 2>mount.err &&
	test_grep -E "^mounted " mount.out &&
	test_grep "hello erofs" "$work_dir/README.txt" &&
	test_path_is_file "$work_dir/sub/inner.txt" &&
	echo from-incr >"$work_dir/extra.txt" &&
	(
		cd "$work_dir" &&
		"$tiler_bin" build --incremental 2>"$HOME/incr.err"
	) >incr.out &&
	test_grep -E "^incr alice/erofs [0-9a-f]+" incr.out &&
	layer=$(cut -d" " -f3 incr.out) &&
	test -n "$layer" &&
	test "$layer" != "$id1" &&
	test_path_is_file "$img_dir/_objects/$id1/image.sqsh" &&
	test_path_is_file "$img_dir/_objects/$layer/image.sqsh" &&
	test_grep -E "^[[:space:]]+compression: zstd$" "$meta_file" &&
	"$tiler_bin" images show alice/erofs >show-incr.out 2>show-incr.err &&
	test_grep -E "^  format +squashfs/zstd$" show-incr.out &&
	test_grep -E "^  parent +$id1$" show-incr.out
'

test_expect_success TILER,LINUX,EROFS_TOOLCHAIN,SQUASHFS_PACK 'a fresh remount serves the mixed-format chain' '
	"$tiler_bin" umount "$work_dir" >umount.out 2>umount.err &&
	test_grep -E "^unmounted " umount.out &&
	mkdir -p "$verify_dir" &&
	"$tiler_bin" mount alice/erofs "$verify_dir" >remount.out 2>remount.err &&
	test_grep -E "^mounted " remount.out &&
	test_grep "hello erofs" "$verify_dir/README.txt" &&
	test_grep from-incr "$verify_dir/extra.txt" &&
	"$tiler_bin" umount "$verify_dir" >umount2.out 2>umount2.err &&
	test_grep -E "^unmounted " umount2.out
'

# 300 KiB of incompressible bytes so a 64k split spans more than one
# block. The project config still selects erofs; --split does not
# change the container. The payload lives in _blocks/<id>/, not
# image.sqsh.
test_expect_success TILER,LINUX,EROFS_TOOLCHAIN 'project image_format erofs --split publishes blocks' '
	mkdir -p "$blk_src" &&
	echo "hello blocks" >"$blk_src/README.txt" &&
	dd if=/dev/urandom of="$blk_src/bulk.bin" bs=1024 count=300 status=none &&
	(
		cd "$proj" &&
		"$tiler_bin" build --split 64k --image alice/blocks "$blk_src" 2>"$HOME/blk.err"
	) >blk.out &&
	test_grep -E "^full alice/blocks [0-9a-f]+" blk.out &&
	blk_id=$(cut -d" " -f3 blk.out) &&
	test -n "$blk_id" &&
	test_path_is_dir "$blk_dir/_objects/$blk_id" &&
	set -- "$blk_dir/_objects/$blk_id"/blk.[0-9][0-9][0-9] &&
	test $# -gt 1 &&
	test_path_is_file "$blk_dir/_objects/$blk_id/blk.000" &&
	test_path_is_missing "$blk_dir/image.sqsh" &&
	test_grep -E "^[[:space:]]+type: erofs$" "$blk_meta" &&
	test_grep -E "^[[:space:]]+compression: $erofs_comp$" "$blk_meta" &&
	"$tiler_bin" images show alice/blocks >show-blk.out 2>show-blk.err &&
	test_grep -E "^  format +erofs/$erofs_comp$" show-blk.out &&
	test_grep -E "^  payload +blocks$" show-blk.out &&
	mkdir -p "$blk_work" &&
	"$tiler_bin" mount alice/blocks "$blk_work" >blk-mount.out 2>blk-mount.err &&
	test_grep -E "^mounted " blk-mount.out &&
	test_grep "hello blocks" "$blk_work/README.txt" &&
	test_path_is_file "$blk_work/bulk.bin" &&
	"$tiler_bin" umount "$blk_work" >blk-umount.out 2>blk-umount.err &&
	test_grep -E "^unmounted " blk-umount.out
'

# --subdir makes sub/ the mount root. The image-root README must not
# leak through, and a read-only view rejects an upper write.
test_expect_success TILER,LINUX,EROFS_TOOLCHAIN,SQUASHFS_PACK 'subdir mount exposes only that directory' '
	mkdir -p "$sub_dir" &&
	"$tiler_bin" mount --read-only --subdir sub alice/erofs "$sub_dir" >sub.out 2>sub.err &&
	test_grep -E "^mounted " sub.out &&
	test_grep inner "$sub_dir/inner.txt" &&
	test_path_is_missing "$sub_dir/README.txt" &&
	! (echo nope >"$sub_dir/nope.txt") &&
	test_path_is_missing "$sub_dir/nope.txt" &&
	"$tiler_bin" umount "$sub_dir" >sub-umount.out 2>sub-umount.err &&
	test_grep -E "^unmounted " sub-umount.out
'

# Hide the squashfs stack. One complete erofs stack is enough for
# doctor to exit 0: image_toolchain warns, and squashfuse_subdir
# warns because squashfuse_ll is not on this PATH.
test_expect_success TILER,LINUX,EROFS_TOOLCHAIN 'doctor passes with only the erofs stack' '
	only=$HOME/erofs-only &&
	mkdir -p "$only" &&
	ln -s "$(command -v mkfs.erofs)" "$only/mkfs.erofs" &&
	ln -s "$(command -v erofsfuse)" "$only/erofsfuse" &&
	ln -s "$(command -v fuse-overlayfs)" "$only/fuse-overlayfs" &&
	ln -s "$(command -v "$fusermount3_bin")" "$only/fusermount3" &&
	ln -s "$(command -v git)" "$only/git" &&
	PATH="$only" "$tiler_bin" doctor >doctor-stack.out 2>doctor-stack.err &&
	test_grep -E "^  image_toolchain +warn " doctor-stack.out &&
	test_grep "erofs ready" doctor-stack.out &&
	test_grep "squashfs missing" doctor-stack.out &&
	test_grep -E "^  squashfuse_subdir +warn " doctor-stack.out &&
	test_grep ! "failing probes" doctor-stack.err
'

test_done
