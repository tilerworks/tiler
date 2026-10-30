#!/bin/sh
# Named-session metadata and validation work without a mount toolchain.
# The mounted lifecycle shares dev across several views and checks that
# close, reopen refusal, and removal preserve the expected record state.

test_description='tiler session new/list/show/close/remove drive a named lifecycle'

. "$(dirname "$0")/lib-tiler.sh"

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
# created below persists across the mounted lifecycle blocks. Independent
# rejection tests use separate records or names that never existed.

# A fresh named session starts active with no mounts. It will be shared
# by the later mounted lifecycle tests.
test_expect_success TILER 'session new creates an active record' '
	"$tiler_bin" session new dev >new.out 2>new.err &&
	test_grep -E "^created dev$" new.out &&
	"$tiler_bin" session list >list1.out 2>list1.err &&
	test_grep -E "^dev[[:space:]]+active[[:space:]]+0[[:space:]]" list1.out
'

# This fixture belongs only to the rejection test and is cleaned even if
# an assertion fails; it does not depend on the mounted lifecycle below.
test_expect_success TILER 'duplicate creation preserves the existing record' '
	test_when_finished "\"$tiler_bin\" session close duplicate &&
		\"$tiler_bin\" session remove duplicate" &&
	"$tiler_bin" session new duplicate >duplicate.out 2>duplicate.err &&
	cp "$HOME/.tiler/sessions/duplicate/session.json" duplicate.before &&
	test_expect_code 1 "$tiler_bin" session new duplicate >dup.out 2>dup.err &&
	test_must_be_empty dup.out &&
	test_grep "already exists" dup.err &&
	test_cmp duplicate.before "$HOME/.tiler/sessions/duplicate/session.json"
'

test_expect_success TILER 'session new rejects a name containing a path separator' '
	test_expect_code 2 "$tiler_bin" session new a/b >bad.out 2>bad.err &&
	test_must_be_empty bad.out &&
	test_grep "invalid session name" bad.err &&
	test_path_is_missing "$HOME/.tiler/sessions/a"
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
	test_grep -E "^full alice/app [0-9a-f]+" build.out &&
	"$tiler_bin" build --image bob/tools "$src2_dir" >build2.out 2>build2.err &&
	test_grep -E "^full bob/tools [0-9a-f]+" build2.out &&
	mkdir -p "$s1_dir" "$s2_dir" "$s3_dir" &&
	"$tiler_bin" mount --session dev alice/app "$s1_dir" >m1.out 2>m1.err &&
	test_grep -E "^mounted " m1.out &&
	"$tiler_bin" mount --session dev --read-only bob/tools "$s2_dir" >m2.out 2>m2.err &&
	test_grep -E "^mounted " m2.out &&
	TILER_SESSION_ID=dev "$tiler_bin" mount alice/app "$s3_dir" >m3.out 2>m3.err &&
	test_grep -E "^mounted " m3.out &&
	test_path_exists "$s1_dir/README.txt" &&
	test_path_exists "$s2_dir/TOOLS.txt"
'

# list renders an aligned table row and show renders a multi-line
# record plus one indented action line per mount, in seq order. Both
# have a --json form. The mountpoint is an absolute path that may
# contain spaces, so the action regex wildcards it with `.*`.
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'session list and show render the record' '
	"$tiler_bin" session list >list3.out 2>list3.err &&
	test_grep -E "^dev[[:space:]]+active[[:space:]]+3[[:space:]]" list3.out &&
	"$tiler_bin" session show dev >show3.out 2>show3.err &&
	test_grep -E "^dev · active" show3.out &&
	test_grep -E "Last activity:" show3.out &&
	test_grep -E "^  Created:" show3.out &&
	test_grep -E "📁 alice/app:.*\[rw · mounted · healthy\]" show3.out &&
	test_grep -E "📁 bob/tools:.*\[ro · mounted · healthy\]" show3.out &&
	test_grep -E "highest priority first" show3.out &&
	"$tiler_bin" session show dev --json >showj.out 2>showj.err &&
	test_grep "\"actions\"" showj.out &&
	test_grep "dev" showj.out &&
	"$tiler_bin" session list --json >listj.out 2>listj.err &&
	test_grep "\"sessions\"" listj.out &&
	test_grep -E "\"mounts\": 3" listj.out
'

# Missing-record errors do not need the shared image or any real mounts.
for tiler_session_operation in show open remove
do
	test_expect_success TILER "session $tiler_session_operation refuses a missing record" '
		test_expect_code 1 "$tiler_bin" session "$tiler_session_operation" nosuch >missing.out 2>missing.err &&
		test_must_be_empty missing.out &&
		test_grep "not found" missing.err &&
		test_path_is_missing "$HOME/.tiler/sessions/nosuch/session.json"
	'
done

# An active record still holds mounts, so remove refuses it and the
# record survives unchanged.
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'session remove refuses an active record' '
	cp "$HOME/.tiler/sessions/dev/session.json" active.before &&
	test_expect_code 1 "$tiler_bin" session remove dev >rmact.out 2>rmact.err &&
	test_must_be_empty rmact.out &&
	test_grep "close it before removing" rmact.err &&
	test_cmp active.before "$HOME/.tiler/sessions/dev/session.json" &&
	"$tiler_bin" session list >list5.out 2>list5.err &&
	test_grep -E "^dev[[:space:]]+active[[:space:]]+3[[:space:]]" list5.out
'

# A manual umount of one view does NOT close a named session. It does
# write that action back to unmounted, so mounts= drops from 3 to 2 and
# the record stays active. The live s1 view still serves its lower-layer
# content.
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'umount of one view keeps a named session active' '
	"$tiler_bin" umount "$s2_dir" >um2.out 2>um2.err &&
	test_grep -E "^unmounted " um2.out &&
	"$tiler_bin" session list >list6.out 2>list6.err &&
	test_grep -E "^dev[[:space:]]+active[[:space:]]+2[[:space:]]" list6.out &&
	test_path_exists "$s1_dir/README.txt"
'

# close tears the views down in reverse seq order, flips the record to
# closed (mounts=0, every action written back to unmounted), and keeps it
# for audit. Closing an already-closed record is an idempotent no-op that
# still prints the confirmation.
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'session close tears down views and is idempotent' '
	"$tiler_bin" session close dev >close.out 2>close.err &&
	test_grep -E "^closed dev$" close.out &&
	test_path_is_missing "$s1_dir/README.txt" &&
	"$tiler_bin" session list >list7.out 2>list7.err &&
	test_grep -E "^dev[[:space:]]+closed[[:space:]]+0[[:space:]]" list7.out &&
	"$tiler_bin" session close dev >close2.out 2>close2.err &&
	test_grep -E "^closed dev$" close2.out
'

# GC of an idle closed session only prints the candidate. Without
# --yes nothing is removed, so the later remove still has a record.
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'session gc previews a closed session without removing it' '
	"$tiler_bin" session gc --expire 0 >gc.out 2>gc.err &&
	test_grep "Expired sessions" gc.out &&
	test_grep "  dev  " gc.out &&
	test_grep "runtime/" gc.out &&
	test_grep "Nothing removed" gc.err &&
	test_grep ! -E "^removed " gc.out &&
	"$tiler_bin" session list >listgc.out 2>listgc.err &&
	test_grep -E "^dev[[:space:]]+closed[[:space:]]+0[[:space:]]" listgc.out
'

# A closed record never accepts a new mount; the caller must create a
# fresh session. The record is left untouched.
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'mount into a closed session is refused' '
	cp "$HOME/.tiler/sessions/dev/session.json" closed.before &&
	test_expect_code 1 "$tiler_bin" mount --session dev alice/app "$s1_dir" >mclosed.out 2>mclosed.err &&
	test_must_be_empty mclosed.out &&
	test_grep "is closed" mclosed.err &&
	test_cmp closed.before "$HOME/.tiler/sessions/dev/session.json" &&
	"$tiler_bin" session list >list8.out 2>list8.err &&
	test_grep -E "^dev[[:space:]]+closed[[:space:]]+0[[:space:]]" list8.out
'

# A closed record removes cleanly and drops out of the listing; removing
# it again is refused as a miss.
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'session remove deletes a closed record' '
	"$tiler_bin" session remove dev >rmdev.out 2>rmdev.err &&
	test_grep -E "^removed dev$" rmdev.out &&
	"$tiler_bin" session list >list9.out 2>list9.err &&
	test_grep ! -E "^dev " list9.out &&
	test_must_fail "$tiler_bin" session remove dev >rmdev2.out 2>rmdev2.err &&
	test_grep "not found" rmdev2.err
'

test_done
