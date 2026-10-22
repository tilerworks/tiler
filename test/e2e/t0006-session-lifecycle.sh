#!/bin/sh
#
# tiler end-to-end test: named session lifecycle and error branches.
#
# Companion to t0005-images-ls-gc-rm.sh. Where t0005 exercises the
# library-management subcommands, this case drives the `session`
# command end to end over real mounts:
#
#   1. session new creates an active record; a duplicate is refused
#      and an invalid name is a usage error,
#   2. mount --session and TILER_SESSION_ID group several views,
#   3. session list and session show render the record (and --json),
#   4. session show/open refuse a missing record,
#   5. session remove refuses an active record,
#   6. umount of one view does not auto-close a named session,
#   7. session close tears the views down, keeps the record, and is
#      idempotent,
#   8. mount into a closed session is refused,
#   9. session remove deletes a closed record and refuses a missing one.
#
# It mirrors the named-session scenarios of the out-of-repo tiler-demo
# smoke harness (run_tests.sh S09/S10) and the tiler-e2e s04-s06 steps,
# re-expressed in the vendored git-test-lib framework so `make e2e`
# runs it.
#
# A real mount needs a writable /dev/fuse plus the squashfs and overlay
# toolchain that `tiler doctor` treats as fail-level. FUSE_TOOLCHAIN from
# sharness.d/tiler.sh models exactly that, so the case skips cleanly -- rather
# than false-fails -- on a host without the toolchain.
#
# Scope guard: this case asserts the named-session lifecycle and its
# error branches only. It does not cover crash recovery (t0007), the
# generated sessions (t0008), git-backed images, split storage,
# or cache/subdir mount variants.

test_description='tiler session new/list/show/close/remove drive a named lifecycle'

# Resolve paths against this script's location BEFORE sourcing the
# framework: test-lib.sh moves the working directory into a per-run
# trash directory and repoints HOME there.
tiler_case_dir=$(cd "$(dirname "$0")" && pwd)
TEST_LIB_DIRECTORY=$(cd "$tiler_case_dir/../git-test-lib" && pwd)
export TEST_LIB_DIRECTORY
TEST_DIRECTORY=$tiler_case_dir
export TEST_DIRECTORY

. "$TEST_LIB_DIRECTORY/test-lib.sh"

# tiler_bin, TILER, LINUX, and FUSE_TOOLCHAIN come from sharness.d/tiler.sh.

# HOME is the per-run trash directory, so tiler's default layout
# ($HOME/.tiler/{images,runtime,sessions,cache}) is already sandboxed and
# needs no --*-dir flags. Mount targets must sit OUTSIDE the runtime tree
# and the reserved directories, so keep them under $HOME/work. src_dir
# builds alice/app and src2_dir builds bob/tools; s1/s2/s3 are the three
# views grouped into the named session `dev`.
src_dir=$HOME/src/app
src2_dir=$HOME/src/tools
s1_dir=$HOME/work/s1
s2_dir=$HOME/work/s2
s3_dir=$HOME/work/s3

# Guarantee every FUSE mount is torn down before the framework removes
# the trash directory, even if an assertion fails or the run is
# interrupted. `tiler umount` is idempotent; fusermount3 -uz is the
# fallback for a view whose daemon already died.
if test -n "$tiler_bin"
then
	test_atexit '
		for _m in "$s1_dir" "$s2_dir" "$s3_dir"
		do
			"$tiler_bin" umount "$_m" >/dev/null 2>&1 ||
			"$fusermount3_bin" -uz "$_m" >/dev/null 2>&1 ||
			true
		done
	'
fi

# git-test-lib eval's each body in the current shell, so the `dev` record
# created below persists across the later test_expect_success blocks (and
# the .out/.err files persist in the trash working dir too). The nine
# blocks walk one session from creation to removal.

# A fresh named session starts active with no mounts. A second `new` of
# the same name is a runtime error (exit 1); an invalid name containing a
# path separator is a usage error (exit 2).
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'session new creates an active record and rejects duplicates' '
	"$tiler_bin" session new dev >new.out 2>new.err &&
	grep -Eq "^created dev$" new.out &&
	"$tiler_bin" session list >list1.out 2>list1.err &&
	grep -Eq "^dev[[:space:]]+active[[:space:]]+0[[:space:]]" list1.out &&
	status=0 &&
	{ "$tiler_bin" session new dev >dup.out 2>dup.err || status=$?; } &&
	test "$status" -ne 0 &&
	grep -q "already exists" dup.err &&
	status=0 &&
	{ "$tiler_bin" session new a/b >bad.out 2>bad.err || status=$?; } &&
	test "$status" -eq 2 &&
	grep -q "invalid session name" bad.err
'

# mount --session groups views under one named record, and the
# TILER_SESSION_ID environment variable binds a mount the same way. Three
# views (rw, ro, rw) land under `dev`, and the lower-layer content is
# served through each mountpoint.
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'mount --session and TILER_SESSION_ID group views' '
	mkdir -p "$src_dir" "$src2_dir" &&
	echo "hello tiler" >"$src_dir/README.txt" &&
	echo "tools" >"$src2_dir/TOOLS.txt" &&
	"$tiler_bin" build --image alice/app "$src_dir" >build.out 2>build.err &&
	grep -Eq "^full alice/app [0-9a-f]+" build.out &&
	"$tiler_bin" build --image bob/tools "$src2_dir" >build2.out 2>build2.err &&
	grep -Eq "^full bob/tools [0-9a-f]+" build2.out &&
	mkdir -p "$s1_dir" "$s2_dir" "$s3_dir" &&
	"$tiler_bin" mount --session dev alice/app "$s1_dir" >m1.out 2>m1.err &&
	grep -Eq "^mounted " m1.out &&
	"$tiler_bin" mount --session dev --read-only bob/tools "$s2_dir" >m2.out 2>m2.err &&
	grep -Eq "^mounted " m2.out &&
	TILER_SESSION_ID=dev "$tiler_bin" mount alice/app "$s3_dir" >m3.out 2>m3.err &&
	grep -Eq "^mounted " m3.out &&
	test -e "$s1_dir/README.txt" &&
	test -e "$s2_dir/TOOLS.txt"
'

# list renders an aligned table row and show renders a multi-line
# record plus one indented action line per mount, in seq order. Both
# have a --json form. The mountpoint is an absolute path that may
# contain spaces, so the action regex wildcards it with `.*`.
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'session list and show render the record' '
	"$tiler_bin" session list >list3.out 2>list3.err &&
	grep -Eq "^dev[[:space:]]+active[[:space:]]+3[[:space:]]" list3.out &&
	"$tiler_bin" session show dev >show3.out 2>show3.err &&
	grep -Eq "^dev$" show3.out &&
	grep -Eq "^  state +active$" show3.out &&
	grep -Eq "^  created +" show3.out &&
	grep -Eq "^    1  mount  alice/app  .*  mode=rw  status=mounted" show3.out &&
	grep -Eq "^    2  mount  bob/tools  .*  mode=ro  status=mounted" show3.out &&
	grep -Eq "^    3  mount  alice/app  .*  mode=rw  status=mounted" show3.out &&
	"$tiler_bin" session show dev --json >showj.out 2>showj.err &&
	grep -q "\"actions\"" showj.out &&
	grep -q "dev" showj.out &&
	"$tiler_bin" session list --json >listj.out 2>listj.err &&
	grep -q "\"sessions\"" listj.out &&
	grep -Eq "\"mounts\": 3" listj.out
'

# A record that never existed is refused by both show and open with a
# non-zero exit naming the miss.
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'session show and open refuse a missing record' '
	status=0 &&
	{ "$tiler_bin" session show nosuch >showns.out 2>showns.err || status=$?; } &&
	test "$status" -ne 0 &&
	grep -q "not found" showns.err &&
	status=0 &&
	{ "$tiler_bin" session open nosuch >openns.out 2>openns.err || status=$?; } &&
	test "$status" -ne 0 &&
	grep -q "not found" openns.err
'

# An active record still holds mounts, so remove refuses it and the
# record survives unchanged.
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'session remove refuses an active record' '
	status=0 &&
	{ "$tiler_bin" session remove dev >rmact.out 2>rmact.err || status=$?; } &&
	test "$status" -ne 0 &&
	grep -q "close it before removing" rmact.err &&
	"$tiler_bin" session list >list5.out 2>list5.err &&
	grep -Eq "^dev[[:space:]]+active[[:space:]]+3[[:space:]]" list5.out
'

# A manual umount of one view does NOT close a named session. It does
# write that action back to unmounted, so mounts= drops from 3 to 2 and
# the record stays active. The live s1 view still serves its lower-layer
# content.
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'umount of one view keeps a named session active' '
	"$tiler_bin" umount "$s2_dir" >um2.out 2>um2.err &&
	grep -Eq "^unmounted " um2.out &&
	"$tiler_bin" session list >list6.out 2>list6.err &&
	grep -Eq "^dev[[:space:]]+active[[:space:]]+2[[:space:]]" list6.out &&
	test -e "$s1_dir/README.txt"
'

# close tears the views down in reverse seq order, flips the record to
# closed (mounts=0, every action written back to unmounted), and keeps it
# for audit. Closing an already-closed record is an idempotent no-op that
# still prints the confirmation.
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'session close tears down views and is idempotent' '
	"$tiler_bin" session close dev >close.out 2>close.err &&
	grep -Eq "^closed dev$" close.out &&
	test ! -e "$s1_dir/README.txt" &&
	"$tiler_bin" session list >list7.out 2>list7.err &&
	grep -Eq "^dev[[:space:]]+closed[[:space:]]+0[[:space:]]" list7.out &&
	"$tiler_bin" session close dev >close2.out 2>close2.err &&
	grep -Eq "^closed dev$" close2.out
'

# A closed record never accepts a new mount; the caller must create a
# fresh session. The record is left untouched.
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'mount into a closed session is refused' '
	status=0 &&
	{ "$tiler_bin" mount --session dev alice/app "$s1_dir" >mclosed.out 2>mclosed.err || status=$?; } &&
	test "$status" -ne 0 &&
	grep -q "is closed" mclosed.err &&
	"$tiler_bin" session list >list8.out 2>list8.err &&
	grep -Eq "^dev[[:space:]]+closed[[:space:]]+0[[:space:]]" list8.out
'

# A closed record removes cleanly and drops out of the listing; removing
# it again is refused as a miss.
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'session remove deletes a closed record' '
	"$tiler_bin" session remove dev >rmdev.out 2>rmdev.err &&
	grep -Eq "^removed dev$" rmdev.out &&
	"$tiler_bin" session list >list9.out 2>list9.err &&
	! grep -Eq "^dev " list9.out &&
	status=0 &&
	{ "$tiler_bin" session remove dev >rmdev2.out 2>rmdev2.err || status=$?; } &&
	test "$status" -ne 0 &&
	grep -q "not found" rmdev2.err
'

test_done
