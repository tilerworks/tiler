#!/bin/sh
#
# tiler end-to-end test: remounting a different image at the same
# workdir creates a second implicit session with an isolated upper.
#
# Companion to t0008-session-implicit.sh (plain auto-* lifecycle) and
# t0003-image-mount.sh (same-view conflict while still mounted). This
# case drives the session-scoped runtime layout:
#
#   1. mount alice/one at $HOME/work/view, write marker.txt into the
#      upper, and record the first auto-* session name,
#   2. umount closes that implicit session,
#   3. mount alice/two at the same workdir creates a second auto-*
#      session whose upper does not contain marker.txt,
#   4. both sessions keep independent sessions/<name>/runtime/m1/
#      combination directories.
#
# A real mount needs a writable /dev/fuse plus the squashfs and overlay
# toolchain that `tiler doctor` treats as fail-level. The FUSE_TOOLCHAIN
# prereq below models exactly that, so the case skips cleanly -- rather
# than false-fails -- on a host without the toolchain.

test_description='tiler remounts a different image at the same workdir with isolated sessions'

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

test_lazy_prereq FUSE_TOOLCHAIN '
	test -e /dev/fuse &&
	command -v squashfuse_ll >/dev/null 2>&1 &&
	command -v fuse-overlayfs >/dev/null 2>&1 &&
	command -v fusermount3 >/dev/null 2>&1 &&
	command -v mksquashfs >/dev/null 2>&1 &&
	command -v git >/dev/null 2>&1
'

src1_dir=$HOME/src/one
src2_dir=$HOME/src/two
workdir=$HOME/work/view
sessions_root=$HOME/.tiler/sessions

if test -n "$tiler_bin"
then
	test_atexit '
		"$tiler_bin" umount "$workdir" >/dev/null 2>&1 ||
		fusermount3 -uz "$workdir" >/dev/null 2>&1 ||
		true
	'
fi

test_expect_success TILER,FUSE_TOOLCHAIN 'build two distinguishable images' '
	mkdir -p "$src1_dir" "$src2_dir" &&
	echo one >"$src1_dir/who.txt" &&
	echo two >"$src2_dir/who.txt" &&
	"$tiler_bin" build --image alice/one "$src1_dir" >b1.out 2>b1.err &&
	"$tiler_bin" build --image alice/two "$src2_dir" >b2.out 2>b2.err &&
	grep -Eq "^full alice/one [0-9a-f]+" b1.out &&
	grep -Eq "^full alice/two [0-9a-f]+" b2.out
'

test_expect_success TILER,FUSE_TOOLCHAIN 'mount image one, write a marker, then umount' '
	mkdir -p "$workdir" &&
	"$tiler_bin" mount alice/one "$workdir" >m1.out 2>m1.err &&
	grep -Eq "^mounted " m1.out &&
	grep -q one "$workdir/who.txt" &&
	echo one >"$workdir/marker.txt" &&
	test -f "$workdir/marker.txt" &&
	"$tiler_bin" session list >list1.out 2>list1.err &&
	auto1=$(grep -E "^auto-[^ ]+ " list1.out | head -1 | cut -d" " -f1) &&
	test -n "$auto1" &&
	test -d "$sessions_root/$auto1/runtime/m1" &&
	"$tiler_bin" umount "$workdir" >u1.out 2>u1.err &&
	grep -Eq "^unmounted " u1.out &&
	# Persist the first session name for the isolation assertions.
	echo "$auto1" >auto1.name
'

test_expect_success TILER,FUSE_TOOLCHAIN 'mount image two at the same workdir with a fresh upper' '
	auto1=$(cat auto1.name) &&
	"$tiler_bin" mount alice/two "$workdir" >m2.out 2>m2.err &&
	grep -Eq "^mounted " m2.out &&
	grep -q two "$workdir/who.txt" &&
	test ! -e "$workdir/marker.txt" &&
	"$tiler_bin" session list >list2.out 2>list2.err &&
	auto2=$(grep -E "^auto-[^ ]+ active " list2.out | head -1 | cut -d" " -f1) &&
	test -n "$auto2" &&
	test "$auto1" != "$auto2" &&
	grep -E "^auto-[^ ]+ " list2.out | wc -l | grep -Eq "^[2-9]" &&
	test -d "$sessions_root/$auto1/runtime/m1" &&
	test -d "$sessions_root/$auto2/runtime/m1" &&
	test "$sessions_root/$auto1/runtime/m1" != "$sessions_root/$auto2/runtime/m1" &&
	"$tiler_bin" umount "$workdir" >u2.out 2>u2.err &&
	grep -Eq "^unmounted " u2.out
'

test_done
