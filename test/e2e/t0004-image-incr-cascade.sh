#!/bin/sh
# Publish mixed-format sibling increments and verify historical mounts,
# reference recovery, descriptions, and an independent merged full.

test_description='tiler preserves mixed-format sibling history and merges a new full'

. "$(dirname "$0")/lib-tiler.sh"

# tiler_bin, TILER, LINUX, and FUSE_TOOLCHAIN come from sharness.d/tiler.sh.

# HOME is the per-run trash directory, so tiler's default layout
# ($HOME/.tiler/{images,sessions,cache,logs}) is already sandboxed and
# needs no --*-dir flags. Mount targets must sit OUTSIDE the runtime tree
# and the reserved directories, so keep them under $HOME/work.
#
# Both increments use the same live lower, so they are siblings. The fresh
# verify_dir starts without the original upper and proves published content.
src_dir=$HOME/src/app
work_dir=$HOME/work/app
verify_dir=$HOME/work/verify
img_dir=$HOME/.tiler/images/alice/app
meta_file=$img_dir/meta.yaml

# Guarantee every FUSE mount is torn down before the framework removes
# the trash directory, even if an assertion fails or the run is
# interrupted. `tiler umount` is idempotent; fusermount3 -uz is the
# fallback for a view whose daemon already died.
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

# git-test-lib eval's each body in the current shell, so id1/layerA/
# layerB/id2 captured below persist across the later test_expect_success
# blocks (and the .out files persist in the trash working dir too).

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN,EROFS_TOOLCHAIN 'build a base full image' '
	mkdir -p "$src_dir" &&
	echo "hello tiler" >"$src_dir/README.txt" &&
	"$tiler_bin" build --image alice/app "$src_dir" >build.out 2>build.err &&
	test_grep -E "^full alice/app [0-9a-f]+" build.out &&
	id1=$(cut -d" " -f3 build.out) &&
	test -n "$id1" &&
	test_grep -E "^[[:space:]]*- id: $id1$" "$meta_file" &&
	grep "^[[:space:]]*- id:" "$meta_file" >matching-lines &&
	test_line_count = 1 matching-lines
'

# build --incremental packs the live view upper as-is and leaves it in place, so
# the two increments differ: layerA holds {a.txt} and layerB holds
# {a.txt,b.txt}. Different content means a different digest, so the two
# layer ids must differ.
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN,EROFS_TOOLCHAIN 'mount and append two distinct increments' '
	mkdir -p "$work_dir" &&
	"$tiler_bin" mount alice/app "$work_dir" >mount.out 2>mount.err &&
	test_grep -E "^mounted " mount.out &&
	test_grep "hello tiler" "$work_dir/README.txt" &&
	echo a >"$work_dir/a.txt" &&
	(
		cd "$work_dir" &&
		"$tiler_bin" build --incremental --format erofs --compression lz4 2>"$HOME/incrA.err"
	) >incrA.out &&
	test_grep -E "^incr alice/app [0-9a-f]+" incrA.out &&
	layerA=$(cut -d" " -f3 incrA.out) &&
	echo b >"$work_dir/b.txt" &&
	(
		cd "$work_dir" &&
		"$tiler_bin" build --incremental 2>"$HOME/incrB.err"
	) >incrB.out &&
	test_grep -E "^incr alice/app [0-9a-f]+" incrB.out &&
	layerB=$(cut -d" " -f3 incrB.out) &&
	test -n "$layerA" && test -n "$layerB" &&
	test "$layerA" != "$layerB"
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN,EROFS_TOOLCHAIN 'mixed-format siblings retain their shared parent in history' '
	"$tiler_bin" images list -v >ls.out 2>ls.err &&
	test_grep "${layerA%${layerA#??????????}}" ls.out &&
	test_grep "${layerB%${layerB#??????????}}" ls.out &&
	grep "^[[:space:]]*- id:" "$meta_file" >matching-lines &&
	test_line_count = 3 matching-lines &&
	test_grep -E "^[[:space:]]*- id: $layerB$" "$meta_file" &&
	for tiler_child in "$layerA" "$layerB"
	do
		test_grep -E "^[[:space:]]*parent: $id1$" "$img_dir/_objects/$tiler_child/image.yaml" || return 1
	done
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN,EROFS_TOOLCHAIN 'a fresh mount serves the published files without the original upper' '
	"$tiler_bin" umount "$work_dir" &&
	mkdir -p "$verify_dir" &&
	"$tiler_bin" mount alice/app "$verify_dir" >remount.out 2>remount.err &&
	test_grep -E "^mounted " remount.out &&
	test_grep "hello tiler" "$verify_dir/README.txt" &&
	test_path_is_file "$verify_dir/a.txt" &&
	test_path_is_file "$verify_dir/b.txt"
'

# build --full inside the live view packs the whole merged mount point
# into a new full image; the name is inferred from the active mount
# (decide.go fullInView sets SrcDir to the mount point). The merged full
# folds the cumulative sibling tip into a full independent of the base.
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN,EROFS_TOOLCHAIN 'build --full merges the view into a new full' '
	(
		cd "$verify_dir" &&
		"$tiler_bin" build --full 2>"$HOME/merge.err"
	) >merge.out &&
	test_grep -E "^full alice/app [0-9a-f]+" merge.out &&
	id2=$(cut -d" " -f3 merge.out) &&
	test -n "$id2" &&
	test "$id2" != "$id1"
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN,EROFS_TOOLCHAIN 'meta.yaml retains history and records the merged full' '
	test_grep -E "^[[:space:]]*- id: $id2$" "$meta_file" &&
	grep "^[[:space:]]*- id:" "$meta_file" >matching-lines &&
	test_line_count = 4 matching-lines &&
	"$tiler_bin" images show alice/app >ls2.out 2>ls2.err &&
	test_grep -E "^  id +$id2$" ls2.out
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN,EROFS_TOOLCHAIN 'historical mixed-format mounts recover references and descriptions' '
	"$tiler_bin" umount "$verify_dir" && "$tiler_bin" mount "alice/app:${layerB%${layerB#??????????}}" "$verify_dir" &&
	test_path_is_file "$verify_dir/a.txt" && test_path_is_file "$verify_dir/b.txt" && rm "$HOME/.tiler/sessions/.refs/state.json" &&
	test_must_fail "$tiler_bin" image rm "alice/app:$id1" >rm.out 2>rm.err && test_grep "referenced by sessions" rm.err &&
	test_path_is_file "$HOME/.tiler/sessions/.refs/state.json" &&
	printf "First line\n\nSecond line.\n" >desc.txt &&
	"$tiler_bin" images describe --file desc.txt "alice/app:$layerB" &&
	"$tiler_bin" images gc >gchist.out 2>gchist.err &&
	test_grep "No image objects eligible" gchist.out &&
	test_grep ! removed gchist.out &&
	test_grep ! "Type yes" gchist.err &&
	"$tiler_bin" images show "alice/app:$layerB" >ls3.out && test_grep "First line" ls3.out && test_grep "Second line." ls3.out && test_path_is_file "$img_dir/_objects/$id1/image.sqsh"
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN,EROFS_TOOLCHAIN 'the merged full remounts and stands alone' '
	"$tiler_bin" umount "$verify_dir" &&
	"$tiler_bin" mount alice/app "$verify_dir" >remount2.out 2>remount2.err &&
	test_grep -E "^mounted " remount2.out &&
	test_grep "hello tiler" "$verify_dir/README.txt" &&
	test_path_is_file "$verify_dir/a.txt" &&
	test_path_is_file "$verify_dir/b.txt" &&
	"$tiler_bin" images show alice/app >ls4.out 2>ls4.err &&
	test_grep -E "^  id +$id2$" ls4.out
'

test_done
