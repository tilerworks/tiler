#!/bin/sh
#
# tiler end-to-end test: build a dmg, write through its shadow, and
# attach that same shadow again.
#
#   1. build image.dmg from a plain directory,
#   2. a writable mount writes through upper.shadow,
#   3. umount leaves the mountpoint empty and the shadow in place,
#   4. session open attaches that same shadow and the write is there,
#   5. a read-only mount does not create upper.shadow.
#
# When this host cannot use attach -shadow on APFS, doctor names HFS+
# and the creates above already used that filesystem. The case does
# not fail on that difference.
#
# The case lists only TILER,MACOS. Linux make e2e skips it. A Darwin
# host without hdiutil_bin skips the file instead of failing.

test_description='macOS dmg build, shadow write, umount, and read-only mount'

tiler_case_dir=$(cd "$(dirname "$0")" && pwd)
TEST_LIB_DIRECTORY=$(cd "$tiler_case_dir/../git-test-lib" && pwd)
export TEST_LIB_DIRECTORY
TEST_DIRECTORY=$tiler_case_dir
export TEST_DIRECTORY

. "$TEST_LIB_DIRECTORY/test-lib.sh"

# tiler_bin, hdiutil_bin, TILER, and MACOS come from sharness.d/tiler.sh.

if test "$(uname -s)" = Darwin && ! command -v "$hdiutil_bin" >/dev/null 2>&1
then
	skip_all="skipping macOS dmg mount; $hdiutil_bin is missing"
	test_done
fi

# HOME is the per-run trash directory, so the default layout under
# $HOME/.tiler is already sandboxed. The mount target stays outside
# the session tree.
src_dir=$HOME/src/app
work_dir=$HOME/work/app
ro_dir=$HOME/work/ro
image=$HOME/.tiler/images/alice/app/image.dmg
shadow=$HOME/.tiler/sessions/dev/runtime/m1/upper.shadow
ro_shadow=$HOME/.tiler/sessions/reader/runtime/m1/upper.shadow

if test -n "$tiler_bin"
then
	test_atexit '
		for _m in "$work_dir" "$ro_dir"
		do
			"$tiler_bin" umount "$_m" >/dev/null 2>&1 ||
			"$hdiutil_bin" detach "$_m" >/dev/null 2>&1 ||
			true
		done
	'
fi

test_expect_success TILER,MACOS 'build image.dmg from a directory' '
	mkdir -p "$src_dir" &&
	echo "hello tiler" >"$src_dir/README.txt" &&
	"$tiler_bin" session new dev >new.out 2>new.err &&
	grep -Eq "^created dev$" new.out &&
	"$tiler_bin" build --image alice/app "$src_dir" >build.out 2>build.err &&
	grep -Eq "^full alice/app [0-9a-f]+" build.out &&
	test -f "$image" &&
	test ! -e "${image%.dmg}.sqsh"
'

test_expect_success TILER,MACOS 'a writable mount writes through upper.shadow' '
	mkdir -p "$work_dir" &&
	"$tiler_bin" mount --session dev alice/app "$work_dir" >mount.out 2>mount.err &&
	grep -Eq "^mounted " mount.out &&
	grep -q "hello tiler" "$work_dir/README.txt" &&
	echo edited >"$work_dir/NEW.txt" &&
	grep -q edited "$work_dir/NEW.txt" &&
	test -f "$shadow" &&
	shadow_ino=$(stat -f %i "$shadow") &&
	test -n "$shadow_ino"
'

# A host that fell back to HFS+ says so in doctor. The mount above
# already used that filesystem, so the note is the assertion and the
# rest of the case keeps going.
test_expect_success TILER,MACOS 'an HFS+ host is named by doctor' '
	"$tiler_bin" doctor --json >fs.json 2>fs.err &&
	if grep -q "HFS+" fs.json
	then
		grep -F "filesystem HFS+ because attach -shadow cannot use an APFS image" fs.json
	else
		grep -F "\"detail\": \"dmg\"" fs.json
	fi
'

test_expect_success TILER,MACOS 'umount leaves the mountpoint empty and the shadow in place' '
	"$tiler_bin" umount "$work_dir" >umount.out 2>umount.err &&
	grep -Eq "^unmounted " umount.out &&
	test -d "$work_dir" &&
	test ! -e "$work_dir/README.txt" &&
	test ! -e "$work_dir/NEW.txt" &&
	test -z "$(find "$work_dir" -mindepth 1 -print)" &&
	test -f "$shadow" &&
	test "$(stat -f %i "$shadow")" = "$shadow_ino"
'

test_expect_success TILER,MACOS 'a second attach of the same shadow still has the write' '
	"$tiler_bin" session open dev >open.out 2>open.err &&
	grep -Eq "^opened dev$" open.out &&
	grep -q "hello tiler" "$work_dir/README.txt" &&
	grep -q edited "$work_dir/NEW.txt" &&
	test -f "$shadow" &&
	test "$(stat -f %i "$shadow")" = "$shadow_ino"
'

test_expect_success TILER,MACOS 'a read-only mount does not create upper.shadow' '
	mkdir -p "$ro_dir" &&
	"$tiler_bin" session new reader >ro-new.out 2>ro-new.err &&
	grep -Eq "^created reader$" ro-new.out &&
	"$tiler_bin" mount --session reader --read-only alice/app "$ro_dir" >ro.out 2>ro.err &&
	grep -Eq "^mounted " ro.out &&
	grep -q "hello tiler" "$ro_dir/README.txt" &&
	test ! -e "$ro_dir/NEW.txt" &&
	test ! -e "$ro_shadow" &&
	test -f "$shadow"
'

test_done
