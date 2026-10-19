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
#   7. confirm the old full + chain retire into a session-pinned hold,
#   8. remount the merged full and confirm it stands alone.
#
# It mirrors the incremental scenario of the out-of-repo tiler-demo
# smoke harness (run_tests.sh S11), re-expressed in the vendored
# git-test-lib framework so `make e2e` runs it.
#
# A real mount needs a writable /dev/fuse plus the squashfs and overlay
# toolchain that `tiler doctor` treats as fail-level. The FUSE_TOOLCHAIN
# prereq below models exactly that, so the case skips cleanly -- rather
# than false-fails -- on a host without the toolchain.
#
# Scope guard: this case asserts the incremental cascade, the full
# merge, and the meta.yaml/hold side effects only. It does not cover
# sessions, crash recovery, split storage, gc/rm retention (t0005), or
# read-only/subdir/cache mount variants.

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
			fusermount3 -uz "$_m" >/dev/null 2>&1 ||
			true
		done
	'
fi

# git-test-lib eval's each body in the current shell, so id1/layerA/
# layerB/id2 captured below persist across the later test_expect_success
# blocks (and the .out files persist in the trash working dir too).

test_expect_success TILER,FUSE_TOOLCHAIN 'build a base full image' '
	mkdir -p "$src_dir" &&
	echo "hello tiler" >"$src_dir/README.txt" &&
	"$tiler_bin" build --image alice/app "$src_dir" >build.out 2>build.err &&
	grep -Eq "^full alice/app [0-9a-f]+" build.out &&
	id1=$(cut -d" " -f3 build.out) &&
	test -n "$id1" &&
	grep -Eq "^id: \"?$id1\"?$" "$meta_file" &&
	! grep -q "^chain:" "$meta_file"
'

# build --incr packs the live view upper as-is and leaves it in place, so
# the two increments differ: layerA holds {a.txt} and layerB holds
# {a.txt,b.txt}. Different content means a different digest, so the two
# layer ids must differ (cf. TestBuildIncrAppendsLayerToChain).
test_expect_success TILER,FUSE_TOOLCHAIN 'mount and append two distinct increments' '
	mkdir -p "$work_dir" &&
	"$tiler_bin" mount alice/app "$work_dir" >mount.out 2>mount.err &&
	grep -Eq "^mounted " mount.out &&
	grep -q "hello tiler" "$work_dir/README.txt" &&
	echo a >"$work_dir/a.txt" &&
	( cd "$work_dir" && "$tiler_bin" build --incr ) >incrA.out 2>incrA.err &&
	grep -Eq "^incr alice/app [0-9a-f]+" incrA.out &&
	layerA=$(cut -d" " -f3 incrA.out) &&
	echo b >"$work_dir/b.txt" &&
	( cd "$work_dir" && "$tiler_bin" build --incr ) >incrB.out 2>incrB.err &&
	grep -Eq "^incr alice/app [0-9a-f]+" incrB.out &&
	layerB=$(cut -d" " -f3 incrB.out) &&
	test -n "$layerA" && test -n "$layerB" &&
	test "$layerA" != "$layerB"
'

test_expect_success TILER,FUSE_TOOLCHAIN 'the chain cascades in ls and meta.yaml' '
	"$tiler_bin" images ls alice/app >ls.out 2>ls.err &&
	grep -Eq " chain=2$" ls.out &&
	test "$(grep -c "^  incr [0-9a-f]" ls.out)" = 2 &&
	test "$(grep -c "^[[:space:]]*- id:" "$meta_file")" = 2 &&
	grep -Eq "^id: \"?$id1\"?$" "$meta_file"
'

test_expect_success TILER,FUSE_TOOLCHAIN 'a fresh remount serves the full plus both increments' '
	"$tiler_bin" umount "$work_dir" &&
	mkdir -p "$verify_dir" &&
	"$tiler_bin" mount alice/app "$verify_dir" >remount.out 2>remount.err &&
	grep -Eq "^mounted " remount.out &&
	grep -q "hello tiler" "$verify_dir/README.txt" &&
	test -f "$verify_dir/a.txt" &&
	test -f "$verify_dir/b.txt"
'

# build --full inside the live view packs the whole merged mount point
# into a new full image; the name is inferred from the active mount
# (decide.go fullInView sets SrcDir to the mount point). The merged full
# folds README + both increments, so its id differs from the base full.
test_expect_success TILER,FUSE_TOOLCHAIN 'build --full merges the view into a new full' '
	( cd "$verify_dir" && "$tiler_bin" build --full ) >merge.out 2>merge.err &&
	grep -Eq "^full alice/app [0-9a-f]+" merge.out &&
	id2=$(cut -d" " -f3 merge.out) &&
	test -n "$id2" &&
	test "$id2" != "$id1"
'

test_expect_success TILER,FUSE_TOOLCHAIN 'meta.yaml is replaced by the merged full' '
	grep -Eq "^id: \"?$id2\"?$" "$meta_file" &&
	! grep -q "^chain:" "$meta_file" &&
	"$tiler_bin" images ls alice/app >ls2.out 2>ls2.err &&
	grep -Eq " chain=0$" ls2.out
'

# The merge retires the old full + chain. verify_dir is still live, so its
# implicit session pins the retired chain: it lands in hold/<old-id> (still
# serving the live view through open fds) rather than being freed at once.
test_expect_success TILER,FUSE_TOOLCHAIN 'the old full and chain retire into a pinned hold' '
	"$tiler_bin" images ls alice/app --archives >ls3.out 2>ls3.err &&
	grep -Eq "^  hold $id1$" ls3.out &&
	test -f "$img_dir/hold/$id1/image.sqsh"
'

test_expect_success TILER,FUSE_TOOLCHAIN 'the merged full remounts and stands alone' '
	"$tiler_bin" umount "$verify_dir" &&
	"$tiler_bin" mount alice/app "$verify_dir" >remount2.out 2>remount2.err &&
	grep -Eq "^mounted " remount2.out &&
	grep -q "hello tiler" "$verify_dir/README.txt" &&
	test -f "$verify_dir/a.txt" &&
	test -f "$verify_dir/b.txt" &&
	"$tiler_bin" images ls alice/app >ls4.out 2>ls4.err &&
	grep -Eq " chain=0$" ls4.out
'

test_done
