#!/bin/sh
#
# tiler end-to-end test: remounting a different image at the same
# workdir creates a second implicit session with an isolated upper.
#
# Companion to t0008-session-implicit.sh (plain generated sessions) and
# t0003-image-mount.sh (same-view conflict while still mounted). This
# case drives the session-scoped runtime layout:
#
#   1. mount alice/one at $HOME/work/view, write marker.txt into the
#      upper, and record the first generated session name,
#   2. umount retains that session and its upper data,
#   3. mount alice/two at the same workdir creates a second generated
#      session whose upper does not contain marker.txt,
#   4. both sessions keep independent sessions/<name>/runtime/m1/
#      combination directories.
#
# A real mount needs a writable /dev/fuse plus the squashfs and overlay
# toolchain that `tiler doctor` treats as fail-level. FUSE_TOOLCHAIN from
# sharness.d/tiler.sh models exactly that, so the case skips cleanly -- rather
# than false-fails -- on a host without the toolchain.

test_description='tiler remounts a different image at the same workdir with isolated sessions'

. "$(dirname "$0")/lib-tiler.sh"

# tiler_bin, TILER, LINUX, and FUSE_TOOLCHAIN come from sharness.d/tiler.sh.

src1_dir=$HOME/src/one
src2_dir=$HOME/src/two
workdir=$HOME/work/view
sessions_root=$HOME/.tiler/sessions

if test -n "$tiler_bin"
then
	test_atexit '
		"$tiler_bin" umount "$workdir" >/dev/null 2>&1 ||
		"$fusermount3_bin" -uz "$workdir" >/dev/null 2>&1 ||
		true
	'
fi

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'build two distinguishable images' '
	mkdir -p "$src1_dir" "$src2_dir" &&
	echo one >"$src1_dir/who.txt" &&
	echo two >"$src2_dir/who.txt" &&
	"$tiler_bin" build --image alice/one "$src1_dir" >b1.out 2>b1.err &&
	"$tiler_bin" build --image alice/two "$src2_dir" >b2.out 2>b2.err &&
	test_grep -E "^full alice/one [0-9a-f]+" b1.out &&
	test_grep -E "^full alice/two [0-9a-f]+" b2.out
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'mount image one, write a marker, then umount' '
	mkdir -p "$workdir" &&
	"$tiler_bin" mount alice/one "$workdir" >m1.out 2>m1.err &&
	test_grep -E "^mounted " m1.out &&
	test_cmp "$src1_dir/who.txt" "$workdir/who.txt" &&
	echo one >"$workdir/marker.txt" &&
	test_path_is_file "$workdir/marker.txt" &&
	auto1=$(sed -n "s/^new session created: //p" m1.out) &&
	test -n "$auto1" &&
	test_path_is_dir "$sessions_root/$auto1/runtime/m1" &&
	"$tiler_bin" umount "$workdir" >u1.out 2>u1.err &&
	test_grep -E "^unmounted " u1.out &&
	test_path_is_missing "$workdir/marker.txt" &&
	test_cmp "$src1_dir/who.txt" "$sessions_root/$auto1/runtime/m1/upper/marker.txt"
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'mount image two at the same workdir with a fresh upper' '
	"$tiler_bin" mount alice/two "$workdir" >m2.out 2>m2.err &&
	test_grep -E "^mounted " m2.out &&
	test_cmp "$src2_dir/who.txt" "$workdir/who.txt" &&
	test_path_is_missing "$workdir/marker.txt" &&
	auto2=$(sed -n "s/^new session created: //p" m2.out) &&
	test -n "$auto2" &&
	test "$auto1" != "$auto2" &&
	"$tiler_bin" session list >list2.out 2>list2.err &&
	test_grep "^$auto1 " list2.out &&
	test_grep "^$auto2 " list2.out &&
	test_path_is_dir "$sessions_root/$auto1/runtime/m1" &&
	test_path_is_dir "$sessions_root/$auto2/runtime/m1" &&
	test_cmp "$src1_dir/who.txt" "$sessions_root/$auto1/runtime/m1/upper/marker.txt" &&
	test_path_is_missing "$sessions_root/$auto2/runtime/m1/upper/marker.txt" &&
	"$tiler_bin" umount "$workdir" >u2.out 2>u2.err &&
	test_grep -E "^unmounted " u2.out
'

test_done
