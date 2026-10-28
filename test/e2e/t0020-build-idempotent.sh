#!/bin/sh
#
# tiler end-to-end test: build idempotency on Linux.
#
# Packing the same bytes twice must reuse the active id and leave
# meta.yaml unchanged. The case covers three sources:
#
#   1. a plain directory published as a full image,
#   2. a writable mount whose upper is packed as an increment,
#   3. a subdirectory of that mount published under a different
#      --image name as its own full image.
#
# Scope guard: squashfs + overlay only. macOS dmg lives in t0021.

test_description='tiler build is idempotent for full, incr, and diverted subdir'

tiler_case_dir=$(cd "$(dirname "$0")" && pwd)
TEST_LIB_DIRECTORY=$(cd "$tiler_case_dir/../git-test-lib" && pwd)
export TEST_LIB_DIRECTORY
TEST_DIRECTORY=$tiler_case_dir
export TEST_DIRECTORY

. "$TEST_LIB_DIRECTORY/test-lib.sh"

src_dir=$HOME/src/app
work_dir=$HOME/work/app
alice_meta=$HOME/.tiler/images/alice/app/meta.yaml
bob_meta=$HOME/.tiler/images/bob/docs/meta.yaml

if test -n "$tiler_bin"
then
	test_atexit '
		"$tiler_bin" umount "$work_dir" >/dev/null 2>&1 ||
		"$fusermount3_bin" -uz "$work_dir" >/dev/null 2>&1 ||
		true
	'
fi

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'plain directory build publishes alice/app' '
	mkdir -p "$src_dir/docs" &&
	echo "hello tiler" >"$src_dir/README.txt" &&
	echo "# Guide" >"$src_dir/docs/guide.md" &&
	"$tiler_bin" build --image alice/app "$src_dir" >full1.out 2>full1.err &&
	test_grep -E "^full alice/app [0-9a-f]+" full1.out &&
	id1=$(cut -d" " -f3 full1.out) &&
	test -n "$id1" &&
	test_path_is_file "$HOME/.tiler/images/alice/app/_objects/$id1/image.sqsh" &&
	cp "$alice_meta" full1.meta
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'plain directory build is idempotent' '
	id1=$(cut -d" " -f3 full1.out) &&
	test -n "$id1" &&
	"$tiler_bin" build --image alice/app "$src_dir" >full2.out 2>full2.err &&
	test_grep -E "^full alice/app [0-9a-f]+" full2.out &&
	id2=$(cut -d" " -f3 full2.out) &&
	test "$id1" = "$id2" &&
	test_cmp full1.meta "$alice_meta"
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'incremental build of an unchanged upper is idempotent' '
	mkdir -p "$work_dir" &&
	"$tiler_bin" mount alice/app "$work_dir" >mount.out 2>mount.err &&
	test_grep -E "^mounted " mount.out &&
	echo layer >"$work_dir/NEW.txt" &&
	(
		cd "$work_dir" &&
		"$tiler_bin" build --incremental
	) >incr1.out 2>incr1.err &&
	test_grep -E "^incr alice/app [0-9a-f]+" incr1.out &&
	layer=$(cut -d" " -f3 incr1.out) &&
	test -n "$layer" &&
	cp "$alice_meta" incr1.meta &&
	(
		cd "$work_dir" &&
		"$tiler_bin" build --incremental
	) >incr2.out 2>incr2.err &&
	test_grep -E "^incr alice/app [0-9a-f]+" incr2.out &&
	layer2=$(cut -d" " -f3 incr2.out) &&
	test "$layer" = "$layer2" &&
	test_cmp incr1.meta "$alice_meta" &&
	"$tiler_bin" images show alice/app >show.out 2>show.err &&
	test_grep -E "^  parent +$id1$" show.out
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'subdir of a mount packs a different image idempotently' '
	test_path_is_file "$work_dir/docs/guide.md" &&
	"$tiler_bin" build --image bob/docs "$work_dir/docs" >div1.out 2>div1.err &&
	test_grep -E "^full bob/docs [0-9a-f]+" div1.out &&
	bob_id=$(cut -d" " -f3 div1.out) &&
	test -n "$bob_id" &&
	test_path_is_file "$HOME/.tiler/images/bob/docs/_objects/$bob_id/image.sqsh" &&
	cp "$bob_meta" div1.meta &&
	"$tiler_bin" build --image bob/docs "$work_dir/docs" >div2.out 2>div2.err &&
	test_grep -E "^full bob/docs [0-9a-f]+" div2.out &&
	bob_id2=$(cut -d" " -f3 div2.out) &&
	test "$bob_id" = "$bob_id2" &&
	test_cmp div1.meta "$bob_meta" &&
	"$tiler_bin" images show alice/app >alice-show.out 2>alice-show.err &&
	test_grep -E "^  parent +$id1$" alice-show.out
'

test_done
