#!/bin/sh
#
# tiler end-to-end test: build --split <size> and split_size: 0.
#
# Companion to t0004-image-incr-cascade.sh (unsplit incremental cascade)
# and t0003-image-mount.sh (single-file mount). This case drives the
# size-based split surface:
#
#   1. a small --split (64k) full build of an incompressible tree
#      publishes _blocks/<id>/ with more than one blk.NNN and no
#      image.sqsh,
#   2. mounting that image, writing the upper, and building --incremental
#      with the same --split publishes _blocks/<incr-id>/ (not
#      _incr/<id>.sqsh); a fresh remount shows the upper file,
#   4. a build with no --split, and a build under a project
#      split_size: 0, each stay a single image.sqsh.
#
# A real mount needs a writable /dev/fuse plus the squashfs and overlay
# toolchain that `tiler doctor` treats as fail-level. FUSE_TOOLCHAIN from
# sharness.d/tiler.sh models exactly that, so the case skips cleanly -- rather
# than false-fails -- on a host without the toolchain.

test_description='tiler build --split size cuts full and incr into blocks'

. "$(dirname "$0")/lib-tiler.sh"

# tiler_bin, TILER, LINUX, and FUSE_TOOLCHAIN come from sharness.d/tiler.sh.

# HOME is the per-run trash directory, so tiler's default layout
# ($HOME/.tiler/{images,sessions,cache}) is already sandboxed and
# needs no --*-dir flags. Mount targets must sit OUTSIDE the runtime
# tree and the reserved directories, so keep them under $HOME/work.
#
# work_dir hosts the writable view the increment is built in.
# verify_dir is a second, fresh mount point: a remount of work_dir
# reuses that point's upper, so only a brand-new point starts empty
# and proves the extra file came from the incr layer.
src_dir=$HOME/src/split
repl_src=$HOME/src/replace
work_dir=$HOME/work/split
verify_dir=$HOME/work/verify
img_dir=$HOME/.tiler/images/alice/split
meta_file=$img_dir/meta.yaml
plain_src=$HOME/src/plain
plain_dir=$HOME/.tiler/images/bob/plain
zero_root=$HOME/zero
zero_dir=$HOME/.tiler/images/carol/zero

if test -n "$tiler_bin"
then
	test_atexit '
		for _m in "$work_dir" "$verify_dir"
		do
			"$tiler_bin" umount "$_m" >/dev/null 2>&1 ||
			"$fusermount3_bin" -uz "$_m" >/dev/null 2>&1 ||
			true
		done
	'
fi

# 300 KiB of incompressible bytes plus a couple of small files. zstd
# cannot shrink the bulk, so a 64k block size spans more than one
# blk.NNN (the same idea as the in-package split fixtures).
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'full --split 64k publishes more than one block' '
	mkdir -p "$src_dir/docs" &&
	echo "hello tiler" >"$src_dir/README.txt" &&
	echo "# guide" >"$src_dir/docs/guide.md" &&
	dd if=/dev/urandom of="$src_dir/bulk.bin" bs=1024 count=300 status=none &&
	"$tiler_bin" build --split 64k --image alice/split "$src_dir" >build.out 2>build.err &&
	test_grep -E "^full alice/split [0-9a-f]+" build.out &&
	id1=$(cut -d" " -f3 build.out) &&
	test -n "$id1" &&
	test_path_is_dir "$img_dir/_objects/$id1" &&
	set -- "$img_dir/_objects/$id1"/blk.[0-9][0-9][0-9] &&
	test $# -gt 1 &&
	test_path_is_file "$img_dir/_objects/$id1/blk.000" &&
	test_path_is_missing "$img_dir/image.sqsh" &&
	"$tiler_bin" images show alice/split >ls.out 2>ls.err &&
	test_grep -E "^  payload +blocks$" ls.out &&
	test_grep -E "^[[:space:]]*- id: $id1$" "$meta_file" &&
	echo "$id1" >full.id
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'mount, edit the upper, and incr with the same --split' '
	id1=$(cat full.id) &&
	mkdir -p "$work_dir" &&
	"$tiler_bin" mount alice/split "$work_dir" >mount.out 2>mount.err &&
	test_grep -E "^mounted " mount.out &&
	test_grep "hello tiler" "$work_dir/README.txt" &&
	test_path_is_file "$work_dir/docs/guide.md" &&
	test_path_is_file "$work_dir/bulk.bin" &&
	echo from-incr >"$work_dir/extra.txt" &&
	(
		cd "$work_dir" &&
		"$tiler_bin" build -i -s 64k 2>"$HOME/incr.err"
	) >incr.out &&
	test_grep -E "^incr alice/split [0-9a-f]+" incr.out &&
	layer=$(cut -d" " -f3 incr.out) &&
	test -n "$layer" &&
	test "$layer" != "$id1" &&
	test_path_is_dir "$img_dir/_objects/$layer" &&
	test_path_is_file "$img_dir/_objects/$layer/blk.000" &&
	test_path_is_missing "$img_dir/_incr/$layer.sqsh" &&
	test_path_is_missing "$img_dir/_incr" &&
	test_grep -E "^[[:space:]]*- id: \"?$layer\"?$" "$meta_file" &&
	test_grep -E "^[[:space:]]*- name: blk.000$" "$meta_file" &&
	"$tiler_bin" images show alice/split >ls-incr.out 2>ls-incr.err &&
	test_grep -E "^  parent +$id1$" ls-incr.out &&
	test_grep -E "^  payload +blocks$" ls-incr.out &&
	echo "$layer" >layer.id
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'a fresh remount serves the split full plus the incr' '
	"$tiler_bin" umount "$work_dir" >umount.out 2>umount.err &&
	test_grep -E "^unmounted " umount.out &&
	mkdir -p "$verify_dir" &&
	"$tiler_bin" mount alice/split "$verify_dir" >remount.out 2>remount.err &&
	test_grep -E "^mounted " remount.out &&
	test_grep "hello tiler" "$verify_dir/README.txt" &&
	test_path_is_file "$verify_dir/docs/guide.md" &&
	test_path_is_file "$verify_dir/bulk.bin" &&
	test_grep from-incr "$verify_dir/extra.txt" &&
	"$tiler_bin" umount "$verify_dir" >umount2.out 2>umount2.err &&
	test_grep -E "^unmounted " umount2.out
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'a new full preserves the pinned split chain' '
	id1=$(cat full.id) &&
	layer=$(cat layer.id) &&
	"$tiler_bin" mount alice/split "$verify_dir" >pin.out 2>pin.err &&
	test_grep -E "^mounted " pin.out &&
	mkdir -p "$repl_src" &&
	echo replaced >"$repl_src/README.txt" &&
	dd if=/dev/urandom of="$repl_src/bulk.bin" bs=1024 count=300 status=none &&
	"$tiler_bin" build --split 64k --image alice/split "$repl_src" >repl.out 2>repl.err &&
	test_grep -E "^full alice/split [0-9a-f]+" repl.out &&
	id2=$(cut -d" " -f3 repl.out) &&
	test -n "$id2" &&
	test "$id2" != "$id1" &&
	test "$id2" != "$layer" &&
	test_grep -E "^[[:space:]]*- id: $id2$" "$meta_file" &&
	grep "^[[:space:]]*- id:" "$meta_file" >matching-lines &&
	test_line_count = 3 matching-lines &&
	test_path_is_file "$img_dir/_objects/$id2/blk.000" &&
	test_path_is_file "$img_dir/_objects/$id1/blk.000" &&
	"$tiler_bin" images show alice/split >ls-new.out 2>ls-new.err &&
	test_grep -E "^  id +$id2$" ls-new.out &&
	test_grep -E "^  payload +blocks$" ls-new.out &&
	"$tiler_bin" images list >ls-hold.out 2>ls-hold.err &&
	test_grep "${id1%${id1#??????????}}" ls-hold.out &&
	"$tiler_bin" umount "$verify_dir" >unpin.out 2>unpin.err &&
	test_grep -E "^unmounted " unpin.out
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'a build with no --split stays a single file' '
	mkdir -p "$plain_src" &&
	echo plain >"$plain_src/README.txt" &&
	"$tiler_bin" build --image bob/plain "$plain_src" >plain.out 2>plain.err &&
	test_grep -E "^full bob/plain [0-9a-f]+" plain.out &&
	plain_id=$(cut -d" " -f3 plain.out) &&
	test_path_is_file "$plain_dir/_objects/$plain_id/image.sqsh" &&
	test_path_is_missing "$plain_dir/_blocks" &&
	"$tiler_bin" images show bob/plain >ls-plain.out 2>ls-plain.err &&
	test_grep -E "^  payload +single$" ls-plain.out
'

# Project split_size: 0 is an explicit off, same as omitting the key.
# The file sits in the working directory so the upward walk applies it
# without a command-line --split.
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'project split_size 0 stays a single file' '
	mkdir -p "$zero_root/src" &&
	printf "%s\n" "split_size: 0" >"$zero_root/.tiler.yaml" &&
	echo zero >"$zero_root/src/README.txt" &&
	(
		cd "$zero_root" &&
		"$tiler_bin" build --image carol/zero src 2>"$HOME/zero.err"
	) >zero.out &&
	test_grep -E "^full carol/zero [0-9a-f]+" zero.out &&
	zero_id=$(cut -d" " -f3 zero.out) &&
	test_path_is_file "$zero_dir/_objects/$zero_id/image.sqsh" &&
	test_path_is_missing "$zero_dir/_blocks" &&
	"$tiler_bin" images show carol/zero >ls-zero.out 2>ls-zero.err &&
	test_grep -E "^  payload +single$" ls-zero.out
'

test_done
