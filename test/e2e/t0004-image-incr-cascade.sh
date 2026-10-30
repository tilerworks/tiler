#!/bin/sh
#
# tiler end-to-end test: incremental cascade, full merge, meta.yaml.
#
# Companion to t0003-image-mount.sh. Where t0003 proves build + mount +
# idempotent remount + distinct-image conflict, this case drives the
# incremental cascade and the full merge that folds it back down:
#
#   1. build a base full image,
#   2. mount it and append two distinct incremental layers,
#   3. confirm the chain cascades in `images ls` and in meta.yaml,
#   4. remount at a fresh point and confirm full + both increments show,
#   5. merge the live view into a new full with `build --full`,
#   6. confirm meta.yaml is replaced by the new full (chain cleared),
#   8. remount the merged full and confirm it stands alone.
#
# It mirrors the incremental scenario of the out-of-repo tiler-demo
# smoke harness (run_tests.sh S11), re-expressed in the vendored
# git-test-lib framework so `make e2e` runs it.
#
# A real mount needs a writable /dev/fuse plus the squashfs and overlay
# toolchain that `tiler doctor` treats as fail-level. FUSE_TOOLCHAIN from
# sharness.d/tiler.sh models exactly that, so the case skips cleanly --
# rather than false-fails -- on a host without the toolchain.
#
# Scope guard: this case asserts the incremental cascade, the full
# merge, and immutable history only.

test_description='tiler cascades increments and merges them into a new full'

# Resolve paths against this script's location BEFORE sourcing the
# framework: test-lib.sh moves the working directory into a per-run
# trash directory and repoints HOME there.
tiler_case_dir=$(cd "$(dirname "$0")" && pwd)
TEST_LIB_DIRECTORY=$(cd "$tiler_case_dir/../git-test-lib" && pwd)
export TEST_LIB_DIRECTORY
TEST_DIRECTORY=$tiler_case_dir
export TEST_DIRECTORY

. "$TEST_LIB_DIRECTORY/test-lib.sh"

test_have_prereq EROFS_TOOLCHAIN || skip_all='needs the erofs toolchain'

# tiler_bin, TILER, LINUX, and FUSE_TOOLCHAIN come from sharness.d/tiler.sh.

# HOME is the per-run trash directory, so tiler's default layout
# ($HOME/.tiler/{images,sessions,cache,logs}) is already sandboxed and
# needs no --*-dir flags. Mount targets must sit OUTSIDE the runtime tree
# and the reserved directories, so keep them under $HOME/work.
#
# work_dir hosts the writable view the increments are built in. verify_dir
# is a SECOND, fresh mount point used from step 4 on: a remount reuses the
# upper of the same point (workspace prepareRuntime keeps earlier
# changes), so only a brand-new point starts with an empty upper. That
# empty upper is what makes README/a.txt/b.txt provable as coming from the
# full + layerA + layerB cascade rather than from leftover upper writes.
src_dir=$HOME/src/app
work_dir=$HOME/work/app
verify_dir=$HOME/work/verify
img_dir=$HOME/.tiler/images/alice/app
meta_file=$img_dir/meta.yaml
img_logs=$HOME/.tiler/logs/images/alice/app

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

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'build a base full image' '
	mkdir -p "$src_dir" &&
	echo "hello tiler" >"$src_dir/README.txt" &&
	"$tiler_bin" build --image alice/app "$src_dir" >build.out 2>build.err &&
	test_grep -E "^full alice/app [0-9a-f]+" build.out &&
	id1=$(cut -d" " -f3 build.out) &&
	test -n "$id1" &&
		test_grep -E "^[[:space:]]*- id: $id1$" "$meta_file" &&
	test "$(grep -c "^[[:space:]]*- id:" "$meta_file")" = 1
'

# build --incremental packs the live view upper as-is and leaves it in place, so
# the two increments differ: layerA holds {a.txt} and layerB holds
# {a.txt,b.txt}. Different content means a different digest, so the two
# layer ids must differ (cf. TestBuildIncrAppendsLayerToChain).
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'mount and append two distinct increments' '
	mkdir -p "$work_dir" &&
	"$tiler_bin" mount alice/app "$work_dir" >mount.out 2>mount.err &&
	test_grep -E "^mounted " mount.out &&
	test_grep "hello tiler" "$work_dir/README.txt" &&
	echo a >"$work_dir/a.txt" &&
		( cd "$work_dir" && "$tiler_bin" build --incremental --format erofs --compression lz4 ) >incrA.out 2>incrA.err &&
	test_grep -E "^incr alice/app [0-9a-f]+" incrA.out &&
	layerA=$(cut -d" " -f3 incrA.out) &&
	echo b >"$work_dir/b.txt" &&
	( cd "$work_dir" && "$tiler_bin" build --incremental ) >incrB.out 2>incrB.err &&
	test_grep -E "^incr alice/app [0-9a-f]+" incrB.out &&
	layerB=$(cut -d" " -f3 incrB.out) &&
	test -n "$layerA" && test -n "$layerB" &&
	test "$layerA" != "$layerB"
'

# Both increments parent to the full root, so they are siblings. One
# publication stamps the root with the tip's time and leaves the other
# branch untouched. The root records that dependency once.
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'incremental activity reaches the parent and skips the sibling' '
	tip_log=$img_logs/$layerB.log &&
	root_log=$img_logs/$id1.log &&
	sib_log=$img_logs/$layerA.log &&
	test_path_is_file "$tip_log" &&
	tip_time=$(sed -n "s/.*\"time\":\"\\([^\"]*\\)\".*\"event\":\"image.create.incremental\".*\"phase\":\"succeeded\".*/\\1/p" "$tip_log" | tail -n 1) &&
	test -n "$tip_time" &&
	test_grep "\"time\":\"$tip_time\".*\"source_version\":\"$layerB\".*\"source_event\":\"image.create.incremental\"" "$root_log" &&
	test "$(grep -c "\"source_version\":\"$layerB\"" "$root_log")" = 1 &&
	test_grep ! "\"source_version\":\"$layerB\"" "$sib_log" &&
	test_grep "\"event\":\"image.create.full\"" "$root_log"
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'the chain cascades in ls and meta.yaml' '
		"$tiler_bin" images list >ls.out 2>ls.err &&
		test_grep "${layerA%${layerA#??????????}}" ls.out &&
		test_grep "${layerB%${layerB#??????????}}" ls.out &&
	test "$(grep -c "^[[:space:]]*- id:" "$meta_file")" = 3 &&
		test_grep -E "^[[:space:]]*- id: $layerB$" "$meta_file"
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'a fresh remount serves the full plus both increments' '
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
# folds README + both increments, so its id differs from the base full.
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'build --full merges the view into a new full' '
	( cd "$verify_dir" && "$tiler_bin" build --full ) >merge.out 2>merge.err &&
	test_grep -E "^full alice/app [0-9a-f]+" merge.out &&
	id2=$(cut -d" " -f3 merge.out) &&
	test -n "$id2" &&
	test "$id2" != "$id1"
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'meta.yaml retains history and records the merged full' '
		test_grep -E "^[[:space:]]*- id: $id2$" "$meta_file" &&
		test "$(grep -c "^[[:space:]]*- id:" "$meta_file")" = 4 &&
	"$tiler_bin" images show alice/app >ls2.out 2>ls2.err &&
		test_grep -E "^  id +$id2$" ls2.out
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'historical mixed-format mounts recover references and descriptions' '
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

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'the merged full remounts and stands alone' '
	"$tiler_bin" umount "$verify_dir" &&
	"$tiler_bin" mount alice/app "$verify_dir" >remount2.out 2>remount2.err &&
	test_grep -E "^mounted " remount2.out &&
	test_grep "hello tiler" "$verify_dir/README.txt" &&
	test_path_is_file "$verify_dir/a.txt" &&
	test_path_is_file "$verify_dir/b.txt" &&
		"$tiler_bin" images show alice/app >ls4.out 2>ls4.err &&
		test_grep -E "^  id +$id2$" ls4.out
'

# rewrite_activity sets every record time. GC trusts the last line, so
# this is how a test makes one version old or still retained.
rewrite_activity() {
	sed "s/\"time\":\"[^\"]*\"/\"time\":\"$2\"/" "$1" >"$1.rewritten" &&
	mv "$1.rewritten" "$1"
}

# A missing child log is initialized once and refreshes its parent, not
# its sibling. An old parent of a retained child then stays, while the
# old sibling and an old independent full are removed.
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'GC initializes a chain once and keeps ancestors of a retained version' '
	obj=$img_dir/_objects &&
	rm -f "$img_logs/$layerB.log" &&
	"$tiler_bin" images gc --expire 0 >init1.out 2>init1.err &&
	test_grep "kept alice/app:$layerB: initialized" init1.err &&
	test_grep "kept alice/app:$id1: initialized" init1.err &&
	test_grep ! "kept alice/app:$layerA: initialized" init1.err &&
	test_grep "\"event\":\"activity.initialize\"" "$img_logs/$layerB.log" &&
	init_time=$(sed -n "s/.*\"time\":\"\\([^\"]*\\)\".*\"event\":\"activity.initialize\".*/\\1/p" "$img_logs/$layerB.log") &&
	test -n "$init_time" &&
	test_grep "\"time\":\"$init_time\".*\"source_version\":\"$layerB\".*\"source_event\":\"activity.initialize\"" "$img_logs/$id1.log" &&
	test_grep ! "activity.initialize" "$img_logs/$layerA.log" &&
	cp "$img_logs/$id1.log" id1.before &&
	cp "$img_logs/$layerB.log" layerB.before &&
	"$tiler_bin" images gc --expire 0 >init2.out 2>init2.err &&
	test_grep ! initialized init2.err &&
	test_cmp id1.before "$img_logs/$id1.log" &&
	test_cmp layerB.before "$img_logs/$layerB.log" &&
	"$tiler_bin" umount "$verify_dir" &&
	"$tiler_bin" session list >drop.list &&
	while read -r name _
	do
		test "$name" = NAME && continue
		test -n "$name" || continue
		"$tiler_bin" session close "$name" >"close-$name.out" &&
		"$tiler_bin" session remove "$name" >"rm-$name.out" || return 1
	done <drop.list &&
	test_grep ! "image.dependency.activity" "$img_logs/$id2.log" &&
	rewrite_activity "$img_logs/$id1.log" "2020-01-01T00:00:00.000000000Z" &&
	rewrite_activity "$img_logs/$layerA.log" "2020-01-01T00:00:00.000000000Z" &&
	rewrite_activity "$img_logs/$id2.log" "2020-01-01T00:00:00.000000000Z" &&
	rewrite_activity "$img_logs/$layerB.log" "2099-01-01T00:00:00.000000000Z" &&
	"$tiler_bin" images gc --expire 0 --yes >anc.out 2>anc.err &&
	test_grep "removed alice/app:$layerA" anc.out &&
	test_grep "removed alice/app:$id2" anc.out &&
	test_grep ! "removed alice/app:$id1" anc.out &&
	test_grep ! "removed alice/app:$layerB" anc.out &&
	test_path_is_missing "$obj/$layerA" &&
	test_path_is_missing "$obj/$id2" &&
	test_path_is_dir "$obj/$id1" &&
	test_path_is_dir "$obj/$layerB"
'

test_done
