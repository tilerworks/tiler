#!/bin/sh
#
# tiler end-to-end test: --rm sessions delete their record and purge
# their runtime when they close.
#
# Companion to t0006-session-lifecycle.sh (a plain named session close
# keeps the record for audit and never deletes the runtime upper) and
# t0008-session-implicit.sh (a plain generated session stays active
# after its last view unmounts). This case drives the --rm
# opt-in, which flips that behavior: a session marked --rm deletes its
# record AND force-removes the session directory of every view -- the
# sessions/<name>/ tree including runtime/m<seq>/ upper data -- when it
# closes. Three close paths share the teardown:
#
#   1. session new --rm <name>, then session close <name> deletes the
#      record and purges the runtime, upper data included,
#   2. mount --rm creates a generated session marked --rm, and
#      umounting its last view deletes the record and purges the
#      runtime,
#   3. mount --rm refuses a session named by --session or
#      TILER_SESSION_ID (exit 2) and points at 'session new --rm',
#   4. a plain (non---rm) session close keeps the closed record and its
#      runtime upper data, the contrast that proves --rm is opt-in.
#
# A real mount needs a writable /dev/fuse plus the squashfs and overlay
# toolchain that `tiler doctor` treats as fail-level. FUSE_TOOLCHAIN from
# sharness.d/tiler.sh models exactly that, so the case skips cleanly -- rather
# than false-fails -- on a host without the toolchain.
#
# Scope guard: this case asserts the --rm record deletion and runtime
# purge only. It does not cover crash recovery (t0007), the images gc
# purge of a stale --rm record, git-backed images, split storage, or
# multi-view --rm sessions.

test_description='tiler --rm sessions delete the record and purge the runtime on close'

. "$(dirname "$0")/lib-tiler.sh"

# tiler_bin, TILER, LINUX, and FUSE_TOOLCHAIN come from sharness.d/tiler.sh.

# HOME is the per-run trash directory, so tiler's default layout
# ($HOME/.tiler/{images,runtime,sessions,cache}) is already sandboxed and
# needs no --*-dir flags. Mount targets must sit OUTSIDE the reserved
# directories, so keep them under $HOME/work. sessions_root is the merged
# sessions tree whose <name>/ directory --rm removes wholesale.
src_dir=$HOME/src/app
sessions_root=$HOME/.tiler/sessions
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
			"$fusermount3_bin" -uz "$_m" >/dev/null 2>&1 ||
			true
		done
	'
fi

# git-test-lib eval's each body in the current shell, so the image built
# below persists across the later blocks, as do the .out/.err files in
# the trash working directory.

# One full image backs every scenario; a writable view of it lands the
# user data in the runtime upper directory that --rm purges.
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'build a full image for the --rm scenarios' '
	mkdir -p "$src_dir" &&
	echo "hello tiler" >"$src_dir/README.txt" &&
	"$tiler_bin" build --image tmp/auto "$src_dir" >build.out 2>build.err &&
	test_grep -E "^full tmp/auto [0-9a-f]+" build.out
'

# A named --rm session shows the rm marker, and closing it deletes the
# record and purges the runtime directory of the view -- upper data and
# all -- instead of keeping the record closed for audit.
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'session new --rm: close deletes the record and purges the runtime' '
	"$tiler_bin" session new --rm dev >new1.out 2>new1.err &&
	test_grep -E "^created dev$" new1.out &&
	"$tiler_bin" session show dev >show1.out 2>show1.err &&
	test_grep -E "^dev · active" show1.out &&
	test_grep -E "^  Created:" show1.out &&
	test_grep -E "remove on close" show1.out &&
	mkdir -p "$named_dir" &&
	"$tiler_bin" mount --session dev tmp/auto "$named_dir" >m1.out 2>m1.err &&
	test_grep -E "^mounted " m1.out &&
	echo "precious" >"$named_dir/UPPER.txt" &&
	test_path_is_dir "$sessions_root/dev/runtime/m1" &&
	test_path_is_file "$sessions_root/dev/runtime/m1/upper/UPPER.txt" &&
	"$tiler_bin" session close dev >close1.out 2>close1.err &&
	test_grep -E "^removed dev$" close1.out &&
	test_path_is_missing "$sessions_root/dev" &&
	test_path_is_missing "$named_dir/UPPER.txt" &&
	test_expect_code 1 "$tiler_bin" session show dev >show1b.out 2>show1b.err &&
	test_grep "not found" show1b.err &&
	"$tiler_bin" session list >list1.out 2>list1.err &&
	test_grep ! -E "^dev " list1.out
'

# mount --rm marks the generated session it creates; umounting its
# last view auto-closes it, and the --rm marker turns that close into a
# record deletion plus a runtime purge.
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'mount --rm: umounting the last view deletes the record and purges the runtime' '
	mkdir -p "$impl_dir" &&
	"$tiler_bin" mount --rm tmp/auto "$impl_dir" >m2.out 2>m2.err &&
	test_grep -E "^mounted " m2.out &&
	auto_rm=$(sed -n "s/^new session created: //p" m2.out) &&
	test -n "$auto_rm" &&
	echo "scratch" >"$impl_dir/SCRATCH.txt" &&
	test_path_is_dir "$sessions_root/$auto_rm/runtime/m1" &&
	test_path_is_file "$sessions_root/$auto_rm/runtime/m1/upper/SCRATCH.txt" &&
	"$tiler_bin" session show "$auto_rm" >show2.out 2>show2.err &&
	test_grep -E "^$auto_rm · active" show2.out &&
	test_grep -E "^  Created:" show2.out &&
	test_grep -E "remove on close" show2.out &&
	"$tiler_bin" umount "$impl_dir" >um2.out 2>um2.err &&
	test_grep -E "^unmounted " um2.out &&
	test_path_is_missing "$sessions_root/$auto_rm" &&
	"$tiler_bin" session list >list2b.out 2>list2b.err &&
	test_grep ! "$auto_rm" list2b.out
'

# --rm applies only to the implicit session mount creates. Naming one
# with --session or TILER_SESSION_ID is a usage error (exit 2) that
# points at the named-session route, and it records no mount.
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'mount --rm refuses a session named by --session or the environment' '
	"$tiler_bin" session new target >new3.out 2>new3.err &&
	mkdir -p "$usage_dir" &&
	test_expect_code 2 "$tiler_bin" mount --rm --session target tmp/auto "$usage_dir" >u1.out 2>u1.err &&
	test_grep "session new --rm" u1.err &&
	test_grep "TILER_SESSION_ID" u1.err &&
	test_expect_code 2 env TILER_SESSION_ID=target "$tiler_bin" mount --rm tmp/auto "$usage_dir" >u2.out 2>u2.err &&
	test_grep "session new --rm" u2.err &&
	test_path_is_missing "$usage_dir/README.txt" &&
	"$tiler_bin" session show target >show3.out 2>show3.err &&
	test_grep -E "^target · active" show3.out &&
	test_grep -E "^  Created:" show3.out &&
	test_grep ! -E "remove on close" show3.out &&
	test_grep ! -E "📁" show3.out
'

# The contrast that proves --rm is opt-in: a plain session close keeps
# the record in the closed state and leaves the runtime upper data on
# disk, exactly as before --rm existed.
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'a plain session close keeps the record and its runtime upper data' '
	"$tiler_bin" session new ctrl >new4.out 2>new4.err &&
	mkdir -p "$ctrl_dir" &&
	"$tiler_bin" mount --session ctrl tmp/auto "$ctrl_dir" >m4.out 2>m4.err &&
	test_grep -E "^mounted " m4.out &&
	echo "keep me" >"$ctrl_dir/KEEP.txt" &&
	test_path_is_file "$sessions_root/ctrl/runtime/m1/upper/KEEP.txt" &&
	"$tiler_bin" session show ctrl >show4.out 2>show4.err &&
	test_grep -E "^ctrl · active" show4.out &&
	test_grep -E "^  Created:" show4.out &&
	test_grep ! -E "remove on close" show4.out &&
	"$tiler_bin" session close ctrl >close4.out 2>close4.err &&
	test_grep -E "^closed ctrl$" close4.out &&
	"$tiler_bin" session list >list4.out 2>list4.err &&
	test_grep -E "^ctrl[[:space:]]+closed[[:space:]]+0[[:space:]]" list4.out &&
	test_path_is_dir "$sessions_root/ctrl/runtime/m1" &&
	test_path_is_file "$sessions_root/ctrl/runtime/m1/upper/KEEP.txt"
'

test_done
