#!/bin/sh
#
# tiler end-to-end test: --rm sessions delete their record and purge
# their runtime when they close.
#
# Companion to t0006-session-lifecycle.sh (a plain named session close
# keeps the record for audit and never deletes the runtime upper) and
# t0008-session-implicit.sh (a plain implicit auto-* session auto-closes
# into the kept-for-audit closed state). This case drives the --rm
# opt-in, which flips that behavior: a session marked --rm deletes its
# record AND force-removes the runtime directory of every view -- the
# <RuntimeDir>/up-<dev>-<ino> tree, upper data included -- when it
# closes. Three close paths share the teardown:
#
#   1. session new --rm <name>, then session close <name> deletes the
#      record and purges the runtime, upper data included,
#   2. mount --rm creates an implicit auto-* session marked --rm, and
#      umounting its last view deletes the record and purges the
#      runtime,
#   3. mount --rm refuses a session named by --session or
#      TILER_SESSION_ID (exit 2) and points at 'session new --rm',
#   4. a plain (non---rm) session close keeps the closed record and its
#      runtime upper data, the contrast that proves --rm is opt-in.
#
# A real mount needs a writable /dev/fuse plus the squashfs and overlay
# toolchain that `tiler doctor` treats as fail-level. The FUSE_TOOLCHAIN
# prereq below models exactly that, so the case skips cleanly -- rather
# than false-fails -- on a host without the toolchain.
#
# Scope guard: this case asserts the --rm record deletion and runtime
# purge only. It does not cover crash recovery (t0007), the images gc
# purge of a stale --rm record, git-backed images, split storage, or
# multi-view --rm sessions.

test_description='tiler --rm sessions delete the record and purge the runtime on close'

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
# and the reserved directories, so keep them under $HOME/work. rt_root is
# the runtime tree whose up-<dev>-<ino> directories --rm purges; each
# scenario derives its own directory name from its mountpoint's device
# and inode, the same derivation the mount engine uses, so the assertion
# names the exact directory rather than guessing.
src_dir=$HOME/src/app
rt_root=$HOME/.tiler/runtime
named_dir=$HOME/work/named
impl_dir=$HOME/work/implicit
usage_dir=$HOME/work/refused
ctrl_dir=$HOME/work/control

# Guarantee every FUSE mount is torn down before the framework removes
# the trash directory, even if an assertion fails or the run is
# interrupted. `tiler umount` is idempotent; fusermount3 -uz is the
# fallback for a view whose daemon already died.
if test -n "$tiler_bin"
then
	test_atexit '
		for _m in "$named_dir" "$impl_dir" "$usage_dir" "$ctrl_dir"
		do
			"$tiler_bin" umount "$_m" >/dev/null 2>&1 ||
			fusermount3 -uz "$_m" >/dev/null 2>&1 ||
			true
		done
	'
fi

# git-test-lib eval's each body in the current shell, so the image built
# below persists across the later blocks, as do the .out/.err files in
# the trash working directory.

# One full image backs every scenario; a writable view of it lands the
# user data in the runtime upper directory that --rm purges.
test_expect_success TILER,FUSE_TOOLCHAIN 'build a full image for the --rm scenarios' '
	mkdir -p "$src_dir" &&
	echo "hello tiler" >"$src_dir/README.txt" &&
	"$tiler_bin" build --image tmp/auto "$src_dir" >build.out 2>build.err &&
	grep -Eq "^full tmp/auto [0-9a-f]+" build.out
'

# A named --rm session shows the rm marker, and closing it deletes the
# record and purges the runtime directory of the view -- upper data and
# all -- instead of keeping the record closed for audit.
test_expect_success TILER,FUSE_TOOLCHAIN 'session new --rm: close deletes the record and purges the runtime' '
	"$tiler_bin" session new --rm dev >new1.out 2>new1.err &&
	grep -Eq "^created dev$" new1.out &&
	"$tiler_bin" session show dev >show1.out 2>show1.err &&
	grep -Eq "^dev active rm created=" show1.out &&
	mkdir -p "$named_dir" &&
	named_rt="up-$(stat -c "%d-%i" "$named_dir")" &&
	"$tiler_bin" mount --session dev tmp/auto "$named_dir" >m1.out 2>m1.err &&
	grep -Eq "^mounted " m1.out &&
	echo "precious" >"$named_dir/UPPER.txt" &&
	test -d "$rt_root/$named_rt" &&
	test -f "$rt_root/$named_rt/upper/UPPER.txt" &&
	"$tiler_bin" session close dev >close1.out 2>close1.err &&
	grep -Eq "^removed dev$" close1.out &&
	test ! -e "$rt_root/$named_rt" &&
	test ! -e "$named_dir/UPPER.txt" &&
	status=0 &&
	{ "$tiler_bin" session show dev >show1b.out 2>show1b.err || status=$?; } &&
	test "$status" -eq 1 &&
	grep -q "not found" show1b.err &&
	"$tiler_bin" session list >list1.out 2>list1.err &&
	! grep -Eq "^dev " list1.out
'

# mount --rm marks the implicit auto-* session it creates; umounting its
# last view auto-closes it, and the --rm marker turns that close into a
# record deletion plus a runtime purge.
test_expect_success TILER,FUSE_TOOLCHAIN 'mount --rm: umounting the last view deletes the record and purges the runtime' '
	mkdir -p "$impl_dir" &&
	impl_rt="up-$(stat -c "%d-%i" "$impl_dir")" &&
	"$tiler_bin" mount --rm tmp/auto "$impl_dir" >m2.out 2>m2.err &&
	grep -Eq "^mounted " m2.out &&
	echo "scratch" >"$impl_dir/SCRATCH.txt" &&
	test -d "$rt_root/$impl_rt" &&
	test -f "$rt_root/$impl_rt/upper/SCRATCH.txt" &&
	"$tiler_bin" session list >list2.out 2>list2.err &&
	auto_rm=$(grep -E "^auto-[^ ]+ active mounts=1$" list2.out | head -1 | cut -d" " -f1) &&
	test -n "$auto_rm" &&
	"$tiler_bin" session show "$auto_rm" >show2.out 2>show2.err &&
	grep -Eq "^auto-[^ ]+ active rm created=" show2.out &&
	"$tiler_bin" umount "$impl_dir" >um2.out 2>um2.err &&
	grep -Eq "^unmounted " um2.out &&
	test ! -e "$rt_root/$impl_rt" &&
	"$tiler_bin" session list >list2b.out 2>list2b.err &&
	! grep -q "$auto_rm" list2b.out
'

# --rm applies only to the implicit session mount creates. Naming one
# with --session or TILER_SESSION_ID is a usage error (exit 2) that
# points at the named-session route, and it records no mount.
test_expect_success TILER,FUSE_TOOLCHAIN 'mount --rm refuses a session named by --session or the environment' '
	"$tiler_bin" session new target >new3.out 2>new3.err &&
	mkdir -p "$usage_dir" &&
	status=0 &&
	{ "$tiler_bin" mount --rm --session target tmp/auto "$usage_dir" >u1.out 2>u1.err || status=$?; } &&
	test "$status" -eq 2 &&
	grep -q "session new --rm" u1.err &&
	grep -q "TILER_SESSION_ID" u1.err &&
	status=0 &&
	{ TILER_SESSION_ID=target "$tiler_bin" mount --rm tmp/auto "$usage_dir" >u2.out 2>u2.err || status=$?; } &&
	test "$status" -eq 2 &&
	grep -q "session new --rm" u2.err &&
	test ! -e "$usage_dir/README.txt" &&
	"$tiler_bin" session show target >show3.out 2>show3.err &&
	grep -Eq "^target active created=" show3.out &&
	! grep -Eq "^  1 mount" show3.out
'

# The contrast that proves --rm is opt-in: a plain session close keeps
# the record in the closed state and leaves the runtime upper data on
# disk, exactly as before --rm existed.
test_expect_success TILER,FUSE_TOOLCHAIN 'a plain session close keeps the record and its runtime upper data' '
	"$tiler_bin" session new ctrl >new4.out 2>new4.err &&
	mkdir -p "$ctrl_dir" &&
	ctrl_rt="up-$(stat -c "%d-%i" "$ctrl_dir")" &&
	"$tiler_bin" mount --session ctrl tmp/auto "$ctrl_dir" >m4.out 2>m4.err &&
	grep -Eq "^mounted " m4.out &&
	echo "keep me" >"$ctrl_dir/KEEP.txt" &&
	test -f "$rt_root/$ctrl_rt/upper/KEEP.txt" &&
	"$tiler_bin" session show ctrl >show4.out 2>show4.err &&
	grep -Eq "^ctrl active created=" show4.out &&
	! grep -Eq "^ctrl active rm" show4.out &&
	"$tiler_bin" session close ctrl >close4.out 2>close4.err &&
	grep -Eq "^closed ctrl$" close4.out &&
	"$tiler_bin" session list >list4.out 2>list4.err &&
	grep -Eq "^ctrl closed mounts=0$" list4.out &&
	test -d "$rt_root/$ctrl_rt" &&
	test -f "$rt_root/$ctrl_rt/upper/KEEP.txt"
'

test_done
