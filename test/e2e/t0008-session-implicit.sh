#!/bin/sh
#
# tiler end-to-end test: generated adjective-animal sessions.
#
# Companion to t0005-images-ls-gc-rm.sh (which asserts the images-rm
# view of a generated-session pin) and t0006-session-lifecycle.sh (named
# sessions). This case drives the `session` command's rendering and
# operations over the records that a plain `mount` (with no --session)
# creates:
#
#   1. mount without --session prints "new session created: <name>",
#   2. session show renders it just like a named record (and --json),
#   3. umounting the last view leaves the non--rm session active,
#   4. the session can still be closed and removed explicitly.
#
# It mirrors the auto-session scenario of the out-of-repo tiler-e2e s07
# step, re-expressed in the vendored git-test-lib framework so
# `make e2e` runs it.
#
# A real mount needs a writable /dev/fuse plus the squashfs and overlay
# toolchain that `tiler doctor` treats as fail-level. The FUSE_TOOLCHAIN
# prereq below models exactly that, so the case skips cleanly -- rather
# than false-fails -- on a host without the toolchain.
#
# Scope guard: this case asserts the session-command view of generated
# sessions only. It does not cover named sessions (t0006), crash
# recovery (t0007), the images-rm pin (t0005), git-backed images, split
# storage, or multi-view implicit sessions.

test_description='tiler session list/show/close/remove drive generated sessions'

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
# and the reserved directories, so keep them under $HOME/work. a1_dir and
# a2_dir each get their own generated session.
src_dir=$HOME/src/app
a1_dir=$HOME/work/auto1
a2_dir=$HOME/work/auto2

# Guarantee every FUSE mount is torn down before the framework removes
# the trash directory, even if an assertion fails or the run is
# interrupted. `tiler umount` is idempotent; fusermount3 -uz is the
# fallback for a view whose daemon already died.
if test -n "$tiler_bin"
then
	test_atexit '
		for _m in "$a1_dir" "$a2_dir"
		do
			"$tiler_bin" umount "$_m" >/dev/null 2>&1 ||
			fusermount3 -uz "$_m" >/dev/null 2>&1 ||
			true
		done
	'
fi

# git-test-lib eval's each body in the current shell, so the names
# captured below persist across the later blocks. A generated name is
# adjective-animal, taken from the "new session created:" line.

# A plain mount with no --session and no TILER_SESSION_ID creates a
# generated record that list renders exactly like a named one.
test_expect_success TILER,FUSE_TOOLCHAIN 'mount without --session creates a generated session' '
	mkdir -p "$src_dir" &&
	echo "hello tiler" >"$src_dir/README.txt" &&
	"$tiler_bin" build --image tmp/auto "$src_dir" >build.out 2>build.err &&
	grep -Eq "^full tmp/auto [0-9a-f]+" build.out &&
	mkdir -p "$a1_dir" &&
	"$tiler_bin" mount tmp/auto "$a1_dir" >m1.out 2>m1.err &&
	grep -Eq "^new session created: [a-z]+-[a-z]+[0-9]?$" m1.out &&
	grep -Eq "^mounted " m1.out &&
	auto1=$(sed -n "s/^new session created: //p" m1.out) &&
	test -n "$auto1" &&
	test -e "$a1_dir/README.txt" &&
	"$tiler_bin" session list >list1.out 2>list1.err &&
	grep -Eq "^$auto1[[:space:]]+active[[:space:]]+1[[:space:]]" list1.out
'

# show renders the generated record with the same header + action shape
# as a named one, and has a --json form.
test_expect_success TILER,FUSE_TOOLCHAIN 'session show renders a generated record' '
	"$tiler_bin" session show "$auto1" >show2.out 2>show2.err &&
	grep -Eq "^$auto1$" show2.out &&
	grep -Eq "^  state +active$" show2.out &&
	grep -Eq "^  created +" show2.out &&
	grep -Eq "^    1  mount  tmp/auto  .*  mode=rw  status=mounted" show2.out &&
	"$tiler_bin" session show "$auto1" --json >showj.out 2>showj.err &&
	grep -q "\"actions\"" showj.out &&
	grep -q "tmp/auto" showj.out
'

# Umounting the last view of a non--rm session does not close it: the
# record stays active with mounts=0.
test_expect_success TILER,FUSE_TOOLCHAIN 'umounting the last view keeps a non-rm session active' '
	"$tiler_bin" umount "$a1_dir" >um1.out 2>um1.err &&
	grep -Eq "^unmounted " um1.out &&
	"$tiler_bin" session list >list3.out 2>list3.err &&
	grep -Eq "^$auto1[[:space:]]+active[[:space:]]+0[[:space:]]" list3.out
'

# A generated session is still an ordinary record: it can be closed and
# removed by name. A second mount mints a distinct name.
test_expect_success TILER,FUSE_TOOLCHAIN 'a generated session can be closed and removed by name' '
	mkdir -p "$a2_dir" &&
	"$tiler_bin" mount tmp/auto "$a2_dir" >m2.out 2>m2.err &&
	grep -Eq "^mounted " m2.out &&
	auto2=$(sed -n "s/^new session created: //p" m2.out) &&
	test -n "$auto2" &&
	test "$auto2" != "$auto1" &&
	"$tiler_bin" session close "$auto2" >close4.out 2>close4.err &&
	grep -Eq "^closed " close4.out &&
	test ! -e "$a2_dir/README.txt" &&
	"$tiler_bin" session remove "$auto2" >rm4.out 2>rm4.err &&
	grep -Eq "^removed " rm4.out &&
	"$tiler_bin" session list >list4b.out 2>list4b.err &&
	! grep -q "$auto2" list4b.out
'

test_done
