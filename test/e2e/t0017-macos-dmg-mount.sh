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
# The MACOS and HDIUTIL prerequisites skip these tests on hosts without
# the macOS disk-image toolchain.

test_description='macOS dmg build, shadow write, umount, and read-only mount'

. "$(dirname "$0")/lib-tiler.sh"

# tiler_bin, hdiutil_bin, TILER, and MACOS come from sharness.d/tiler.sh.

# HOME is the per-run trash directory, so the default layout under
# $HOME/.tiler is already sandboxed. The mount target stays outside
# the session tree.
src_dir=$HOME/src/app
work_dir=$HOME/work/app
ro_dir=$HOME/work/ro
img_dir=$HOME/.tiler/images/alice/app
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

test_expect_success TILER,MACOS,HDIUTIL 'build image.dmg from a directory' '
	mkdir -p "$src_dir" &&
	echo "hello tiler" >"$src_dir/README.txt" &&
	"$tiler_bin" session new dev >new.out 2>new.err &&
	test_grep -E "^created dev$" new.out &&
	"$tiler_bin" build --image alice/app "$src_dir" >build.out 2>build.err &&
	test_grep -E "^full alice/app [0-9a-f]+" build.out &&
	id=$(cut -d" " -f3 build.out) &&
	test -n "$id" &&
	image=$img_dir/_objects/$id/image.dmg &&
	test_path_is_file "$image" &&
	test_path_is_missing "${image%.dmg}.sqsh"
'

test_expect_success TILER,MACOS,HDIUTIL 'a writable mount writes through upper.shadow' '
	mkdir -p "$work_dir" &&
	"$tiler_bin" mount --session dev alice/app "$work_dir" >mount.out 2>mount.err &&
	test_grep -E "^mounted " mount.out &&
	test_grep "hello tiler" "$work_dir/README.txt" &&
	echo edited >"$work_dir/NEW.txt" &&
	test_grep edited "$work_dir/NEW.txt" &&
	test_path_is_file "$shadow" &&
	shadow_ino=$(stat -f %i "$shadow") &&
	test -n "$shadow_ino"
'

# A host that fell back to HFS+ says so in doctor. The mount above
# already used that filesystem, so the note is the assertion and the
# rest of the case keeps going.
test_expect_success TILER,MACOS,HDIUTIL 'an HFS+ host is named by doctor' '
	"$tiler_bin" doctor --json >fs.json 2>fs.err &&
	if grep -q "HFS+" fs.json
	then
		test_grep -F "filesystem HFS+ because attach -shadow cannot use an APFS image" fs.json
	else
		test_grep -F "\"detail\": \"dmg\"" fs.json
	fi
'

test_expect_success TILER,MACOS,HDIUTIL 'umount leaves the mountpoint empty and the shadow in place' '
	"$tiler_bin" umount "$work_dir" >umount.out 2>umount.err &&
	test_grep -E "^unmounted " umount.out &&
	test_path_is_dir "$work_dir" &&
	test_path_is_missing "$work_dir/README.txt" &&
	test_path_is_missing "$work_dir/NEW.txt" &&
	find "$work_dir" -mindepth 1 -print >unmounted-files.out &&
	test_must_be_empty unmounted-files.out &&
	test_path_is_file "$shadow" &&
	current_shadow_ino=$(stat -f %i "$shadow") &&
	test "$current_shadow_ino" = "$shadow_ino"
'

test_expect_success TILER,MACOS,HDIUTIL 'a second attach of the same shadow still has the write' '
	"$tiler_bin" session open dev >open.out 2>open.err &&
	test_grep -E "^opened dev$" open.out &&
	test_grep "hello tiler" "$work_dir/README.txt" &&
	test_grep edited "$work_dir/NEW.txt" &&
	test_path_is_file "$shadow" &&
	current_shadow_ino=$(stat -f %i "$shadow") &&
	test "$current_shadow_ino" = "$shadow_ino"
'

test_expect_success TILER,MACOS,HDIUTIL 'a read-only mount does not create upper.shadow' '
	mkdir -p "$ro_dir" &&
	"$tiler_bin" session new reader >ro-new.out 2>ro-new.err &&
	test_grep -E "^created reader$" ro-new.out &&
	"$tiler_bin" mount --session reader --read-only alice/app "$ro_dir" >ro.out 2>ro.err &&
	test_grep -E "^mounted " ro.out &&
	test_grep "hello tiler" "$ro_dir/README.txt" &&
	test_path_is_missing "$ro_dir/NEW.txt" &&
	test_path_is_missing "$ro_shadow" &&
	test_path_is_file "$shadow"
'

test_done
