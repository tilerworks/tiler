#!/bin/sh
#
# tiler end-to-end test: build --split <size> and split_size: 0.
#
# Companion to t0004-image-incr-cascade.sh (unsplit incremental cascade)
# and t0003-image-mount.sh (single-file mount). This case drives the
# size-based split surface:
#
#   1. a small --split (64k) full build of an incompressible tree
#      publishes blocks/<id>/ with more than one blk.NNN and no
#      image.sqsh,
#   2. mounting that image, writing the upper, and building --incremental
#      with the same --split publishes blocks/<incr-id>/ (not
#      incr/<id>.sqsh); a fresh remount shows the upper file,
#   3. a second full --split of the same name, while the first image
#      is still mounted, retires the old full and its incr into a
#      session-pinned hold/<old-id>/blocks/ (not archives),
#   4. a build with no --split, and a build under a project
#      split_size: 0, each stay a single image.sqsh.
#
# A real mount needs a writable /dev/fuse plus the squashfs and overlay
# toolchain that `tiler doctor` treats as fail-level. FUSE_TOOLCHAIN from
# sharness.d/tiler.sh models exactly that, so the case skips cleanly -- rather
# than false-fails -- on a host without the toolchain.

test_description='tiler build --split size cuts full and incr into blocks'

# Resolve paths against this script's location BEFORE sourcing the
# framework: test-lib.sh moves the working directory into a per-run
# trash directory and repoints HOME there.
tiler_case_dir=$(cd "$(dirname "$0")" && pwd)
TEST_LIB_DIRECTORY=$(cd "$tiler_case_dir/../git-test-lib" && pwd)
export TEST_LIB_DIRECTORY
TEST_DIRECTORY=$tiler_case_dir
export TEST_DIRECTORY

. "$TEST_LIB_DIRECTORY/test-lib.sh"

# tiler_bin, TILER, and FUSE_TOOLCHAIN come from sharness.d/tiler.sh.

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
			fusermount3 -uz "$_m" >/dev/null 2>&1 ||
			true
		done
	'
fi

# 300 KiB of incompressible bytes plus a couple of small files. zstd
# cannot shrink the bulk, so a 64k block size spans more than one
# blk.NNN (the same idea as the in-package split fixtures).
test_expect_success TILER,FUSE_TOOLCHAIN 'full --split 64k publishes more than one block' '
	mkdir -p "$src_dir/docs" &&
	echo "hello tiler" >"$src_dir/README.txt" &&
	echo "# guide" >"$src_dir/docs/guide.md" &&
	dd if=/dev/urandom of="$src_dir/bulk.bin" bs=1024 count=300 status=none &&
	"$tiler_bin" build --split 64k --image alice/split "$src_dir" >build.out 2>build.err &&
	grep -Eq "^full alice/split [0-9a-f]+" build.out &&
	id1=$(cut -d" " -f3 build.out) &&
	test -n "$id1" &&
	test -d "$img_dir/blocks/$id1" &&
	set -- "$img_dir/blocks/$id1"/blk.[0-9][0-9][0-9] &&
	test $# -gt 1 &&
	test -f "$img_dir/blocks/$id1/blk.000" &&
	test ! -e "$img_dir/image.sqsh" &&
	"$tiler_bin" images show alice/split >ls.out 2>ls.err &&
	grep -Eq "^  form +blocks$" ls.out &&
	grep -Eq "^  blocks +[2-9][0-9]*$" ls.out &&
	grep -Eq "^id: \"?$id1\"?$" "$meta_file" &&
	echo "$id1" >full.id
'

test_expect_success TILER,FUSE_TOOLCHAIN 'mount, edit the upper, and incr with the same --split' '
	id1=$(cat full.id) &&
	mkdir -p "$work_dir" &&
	"$tiler_bin" mount alice/split "$work_dir" >mount.out 2>mount.err &&
	grep -Eq "^mounted " mount.out &&
	grep -q "hello tiler" "$work_dir/README.txt" &&
	test -f "$work_dir/docs/guide.md" &&
	test -f "$work_dir/bulk.bin" &&
	echo from-incr >"$work_dir/extra.txt" &&
	( cd "$work_dir" && "$tiler_bin" build -i -s 64k ) >incr.out 2>incr.err &&
	grep -Eq "^incr alice/split [0-9a-f]+" incr.out &&
	layer=$(cut -d" " -f3 incr.out) &&
	test -n "$layer" &&
	test "$layer" != "$id1" &&
	test -d "$img_dir/blocks/$layer" &&
	test -f "$img_dir/blocks/$layer/blk.000" &&
	test ! -e "$img_dir/incr/$layer.sqsh" &&
	test ! -d "$img_dir/incr" &&
	grep -Eq "^[[:space:]]*- id: \"?$layer\"?$" "$meta_file" &&
	grep -Eq "^[[:space:]]*- name: blk.000$" "$meta_file" &&
	"$tiler_bin" images show alice/split >ls-incr.out 2>ls-incr.err &&
	grep -Eq "^  chain +1$" ls-incr.out &&
	grep -Eq "^  form +blocks$" ls-incr.out &&
	echo "$layer" >layer.id
'

test_expect_success TILER,FUSE_TOOLCHAIN 'a fresh remount serves the split full plus the incr' '
	"$tiler_bin" umount "$work_dir" >umount.out 2>umount.err &&
	grep -Eq "^unmounted " umount.out &&
	mkdir -p "$verify_dir" &&
	"$tiler_bin" mount alice/split "$verify_dir" >remount.out 2>remount.err &&
	grep -Eq "^mounted " remount.out &&
	grep -q "hello tiler" "$verify_dir/README.txt" &&
	test -f "$verify_dir/docs/guide.md" &&
	test -f "$verify_dir/bulk.bin" &&
	grep -q from-incr "$verify_dir/extra.txt" &&
	"$tiler_bin" umount "$verify_dir" >umount2.out 2>umount2.err &&
	grep -Eq "^unmounted " umount2.out
'

# verify_dir stays mounted across this case so its session pins the
# retired full id. An unpinned replace would archive the chain instead
# of leaving it in hold/.
test_expect_success TILER,FUSE_TOOLCHAIN 'a new full retires the split chain into a pinned hold' '
	id1=$(cat full.id) &&
	layer=$(cat layer.id) &&
	"$tiler_bin" mount alice/split "$verify_dir" >pin.out 2>pin.err &&
	grep -Eq "^mounted " pin.out &&
	mkdir -p "$repl_src" &&
	echo replaced >"$repl_src/README.txt" &&
	dd if=/dev/urandom of="$repl_src/bulk.bin" bs=1024 count=300 status=none &&
	"$tiler_bin" build --split 64k --image alice/split "$repl_src" >repl.out 2>repl.err &&
	grep -Eq "^full alice/split [0-9a-f]+" repl.out &&
	id2=$(cut -d" " -f3 repl.out) &&
	test -n "$id2" &&
	test "$id2" != "$id1" &&
	test "$id2" != "$layer" &&
	grep -Eq "^id: \"?$id2\"?$" "$meta_file" &&
	! grep -q "^chain:" "$meta_file" &&
	test -d "$img_dir/blocks/$id2" &&
	test -f "$img_dir/blocks/$id2/blk.000" &&
	test ! -e "$img_dir/blocks/$id1" &&
	test ! -e "$img_dir/blocks/$layer" &&
	test -f "$img_dir/hold/$id1/meta.yaml" &&
	test -f "$img_dir/hold/$id1/blocks/$id1/blk.000" &&
	test -f "$img_dir/hold/$id1/blocks/$layer/blk.000" &&
	test ! -d "$img_dir/archives" &&
	"$tiler_bin" images show alice/split >ls-new.out 2>ls-new.err &&
	grep -Eq "^  chain +0$" ls-new.out &&
	grep -Eq "^  form +blocks$" ls-new.out &&
	"$tiler_bin" images show alice/split >ls-hold.out 2>ls-hold.err &&
	grep -Eq "^    $id1$" ls-hold.out &&
	"$tiler_bin" umount "$verify_dir" >unpin.out 2>unpin.err &&
	grep -Eq "^unmounted " unpin.out
'

test_expect_success TILER,FUSE_TOOLCHAIN 'a build with no --split stays a single file' '
	mkdir -p "$plain_src" &&
	echo plain >"$plain_src/README.txt" &&
	"$tiler_bin" build --image bob/plain "$plain_src" >plain.out 2>plain.err &&
	grep -Eq "^full bob/plain [0-9a-f]+" plain.out &&
	test -f "$plain_dir/image.sqsh" &&
	test ! -e "$plain_dir/blocks" &&
	"$tiler_bin" images show bob/plain >ls-plain.out 2>ls-plain.err &&
	grep -Eq "^  chain +0$" ls-plain.out &&
	grep -Eq "^  form +single$" ls-plain.out &&
	! grep -Eq "^  form +blocks$" ls-plain.out
'

# Project split_size: 0 is an explicit off, same as omitting the key.
# The file sits in the working directory so the upward walk applies it
# without a command-line --split.
test_expect_success TILER,FUSE_TOOLCHAIN 'project split_size 0 stays a single file' '
	mkdir -p "$zero_root/src" &&
	printf "%s\n" "split_size: 0" >"$zero_root/.tiler.yaml" &&
	echo zero >"$zero_root/src/README.txt" &&
	(
		cd "$zero_root" &&
		"$tiler_bin" build --image carol/zero src
	) >zero.out 2>zero.err &&
	grep -Eq "^full carol/zero [0-9a-f]+" zero.out &&
	test -f "$zero_dir/image.sqsh" &&
	test ! -e "$zero_dir/blocks" &&
	"$tiler_bin" images show carol/zero >ls-zero.out 2>ls-zero.err &&
	grep -Eq "^  form +single$" ls-zero.out &&
	! grep -Eq "^  form +blocks$" ls-zero.out
'

test_done
