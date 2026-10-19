#!/bin/sh
#
# tiler end-to-end test: images ls / gc / rm lifecycle.
#
# Companion to t0004-image-incr-cascade.sh. Where t0004 drives the
# incremental cascade and the full merge, this case exercises the
# library-management subcommands over a real, mounted image:
#
#   1. images ls renders the active image, its chain, and --json,
#   2. images gc promotes an unreferenced hold into an archive,
#   3. images gc --keep-versions expires the older archive,
#   4. images rm deprecates then removes an unreferenced image,
#   5. images rm is refused while an active session references it.
#
# It mirrors the listing, gc, and rm scenarios of the out-of-repo
# tiler-demo smoke harness (run_tests.sh S02, S13, S19), re-expressed in
# the vendored git-test-lib framework so `make e2e` runs it.
#
# A real mount needs a writable /dev/fuse plus the squashfs and overlay
# toolchain that `tiler doctor` treats as fail-level. The FUSE_TOOLCHAIN
# prereq below models exactly that, so the case skips cleanly -- rather
# than false-fails -- on a host without the toolchain. The gc/rm pins
# are created by real mounts, so the whole case is gated on it.
#
# Scope guard: this case asserts ls/gc/rm over the default single-file
# storage only. It does not cover split storage, cache, session
# subcommands, crash recovery, or the incremental cascade (t0004).

test_description='tiler images ls, gc, and rm manage the library lifecycle'

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
# and the reserved directories, so keep them under $HOME/work. work_dir
# carries alice/app through the gc lifecycle; work2_dir carries life/app
# for the rm-referenced refusal.
src_dir=$HOME/src/app
work_dir=$HOME/work/app
work2_dir=$HOME/work/life

# Guarantee every FUSE mount is torn down before the framework removes
# the trash directory, even if an assertion fails or the run is
# interrupted. `tiler umount` is idempotent; fusermount3 -uz is the
# fallback for a view whose daemon already died.
if test -n "$tiler_bin"
then
	test_atexit '
		for _m in "$work_dir" "$work2_dir"
		do
			"$tiler_bin" umount "$_m" >/dev/null 2>&1 ||
			fusermount3 -uz "$_m" >/dev/null 2>&1 ||
			true
		done
	'
fi

# git-test-lib eval's each body in the current shell, so id1/id2 captured
# below persist across the later test_expect_success blocks (and the .out
# files persist in the trash working dir too). id1 and id2 are two full
# image ids built in order, so id1 is the older archive and id2 the newer.

# One active image with a single increment exercises every ls form: the
# all-images line, the per-name chain expansion, and the --json document.
test_expect_success TILER,FUSE_TOOLCHAIN 'images ls lists the active image, its chain, and json' '
	mkdir -p "$src_dir" &&
	echo "hello tiler" >"$src_dir/README.txt" &&
	"$tiler_bin" build --image alice/app "$src_dir" >build.out 2>build.err &&
	grep -Eq "^full alice/app [0-9a-f]+" build.out &&
	id1=$(cut -d" " -f3 build.out) &&
	test -n "$id1" &&
	mkdir -p "$work_dir" &&
	"$tiler_bin" mount alice/app "$work_dir" >mount.out 2>mount.err &&
	grep -Eq "^mounted " mount.out &&
	echo edit >"$work_dir/NEW.txt" &&
	( cd "$work_dir" && "$tiler_bin" build --incr ) >incr.out 2>incr.err &&
	grep -Eq "^incr alice/app [0-9a-f]+" incr.out &&
	"$tiler_bin" images ls >lsall.out 2>lsall.err &&
	grep -q "alice/app" lsall.out &&
	"$tiler_bin" images ls alice/app >ls.out 2>ls.err &&
	grep -Eq " chain=1$" ls.out &&
	grep -Eq "^  incr [0-9a-f]" ls.out &&
	"$tiler_bin" images ls --json >lsjson.out 2>lsjson.err &&
	grep -q "\"images\"" lsjson.out
'

# Rebuilding a mounted image retires the old full + chain into a hold that
# the live view pins. Umounting drops the pin, so the next gc promotes the
# unreferenced hold into an archive (demo S13 "rebuild ... to create an
# archive"). A changed source tree guarantees a distinct new full id.
test_expect_success TILER,FUSE_TOOLCHAIN 'gc promotes an unreferenced hold into an archive' '
	echo "v2" >"$src_dir/version.txt" &&
	"$tiler_bin" build --image alice/app "$src_dir" >build2.out 2>build2.err &&
	grep -Eq "^full alice/app [0-9a-f]+" build2.out &&
	id2=$(cut -d" " -f3 build2.out) &&
	test -n "$id2" && test "$id2" != "$id1" &&
	"$tiler_bin" umount "$work_dir" &&
	"$tiler_bin" images gc >gc.out 2>gc.err &&
	grep -Eq "^archived alice/app $id1$" gc.out &&
	"$tiler_bin" images ls alice/app --archives >lsa.out 2>lsa.err &&
	grep -Eq "^  archive $id1$" lsa.out
'

# A second rebuild+umount+gc cycle leaves two archives, id1 older and id2
# newer. --keep-versions 1 is a root persistent flag injected into the
# resolved config, so gc ranks the archives newest first and expires every
# rank at or past the limit: id2 (rank 0) is kept and id1 (rank 1) removed.
test_expect_success TILER,FUSE_TOOLCHAIN 'gc --keep-versions expires the older archive' '
	"$tiler_bin" mount alice/app "$work_dir" >mount3.out 2>mount3.err &&
	grep -Eq "^mounted " mount3.out &&
	echo "v3" >"$src_dir/version.txt" &&
	"$tiler_bin" build --image alice/app "$src_dir" >build3.out 2>build3.err &&
	grep -Eq "^full alice/app [0-9a-f]+" build3.out &&
	"$tiler_bin" umount "$work_dir" &&
	"$tiler_bin" images gc >gc2.out 2>gc2.err &&
	grep -Eq "^archived alice/app $id2$" gc2.out &&
	"$tiler_bin" images gc --keep-versions 1 >gc3.out 2>gc3.err &&
	grep -Eq "^removed alice/app $id1$" gc3.out &&
	"$tiler_bin" images ls alice/app --archives >lsb.out 2>lsb.err &&
	grep -Eq "^  archive $id2$" lsb.out &&
	! grep -Eq "^  archive $id1$" lsb.out
'

# A never-mounted image carries no session pin, so the two-phase rm runs
# straight through: the first call flags it deprecated, the second removes
# it, and the all-images listing no longer names it.
test_expect_success TILER,FUSE_TOOLCHAIN 'rm deprecates then removes an unreferenced image' '
	"$tiler_bin" build --image tmp/rmtest "$src_dir" >build4.out 2>build4.err &&
	grep -Eq "^full tmp/rmtest [0-9a-f]+" build4.out &&
	"$tiler_bin" images rm tmp/rmtest >rm1.out 2>rm1.err &&
	grep -Eq "^deprecated tmp/rmtest$" rm1.out &&
	"$tiler_bin" images rm tmp/rmtest >rm2.out 2>rm2.err &&
	grep -Eq "^removed tmp/rmtest$" rm2.out &&
	"$tiler_bin" images ls >lsall2.out 2>lsall2.err &&
	! grep -q "tmp/rmtest" lsall2.out
'

# Mounting without --session creates an implicit auto-* session that pins
# the image. While that view is live, rm must be refused with a non-zero
# exit naming the pinning session (demo S19). Umounting closes the session
# and drops the pin, after which the two-phase rm succeeds.
test_expect_success TILER,FUSE_TOOLCHAIN 'rm is refused while an active session references it' '
	"$tiler_bin" build --image life/app "$src_dir" >build5.out 2>build5.err &&
	grep -Eq "^full life/app [0-9a-f]+" build5.out &&
	mkdir -p "$work2_dir" &&
	"$tiler_bin" mount life/app "$work2_dir" >mount5.out 2>mount5.err &&
	grep -Eq "^mounted " mount5.out &&
	status=0 &&
	{ "$tiler_bin" images rm life/app >rmref.out 2>rmref.err || status=$?; } &&
	test "$status" -ne 0 &&
	grep -q "still referenced by session auto-" rmref.err &&
	"$tiler_bin" umount "$work2_dir" &&
	"$tiler_bin" images rm life/app >rm3.out 2>rm3.err &&
	grep -Eq "^deprecated life/app$" rm3.out &&
	"$tiler_bin" images rm life/app >rm4.out 2>rm4.err &&
	grep -Eq "^removed life/app$" rm4.out
'

test_done
