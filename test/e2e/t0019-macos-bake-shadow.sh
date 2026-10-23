#!/bin/sh
#
# tiler end-to-end test: bake a writable dmg shadow into a new image.
#
# Building inside the mounted view publishes a new image.dmg, leaves
# upper.shadow in the session runtime, and does not retarget the live
# attach. A file written immediately before bake is in a read-only
# mount of that published image. images show reports one single-file
# payload and chain length 1.
#
# The case lists only TILER,MACOS. Linux make e2e skips it. A Darwin
# host without hdiutil_bin skips the file instead of failing.

test_description='macOS bake keeps the live shadow and publishes one dmg'

tiler_case_dir=$(cd "$(dirname "$0")" && pwd)
TEST_LIB_DIRECTORY=$(cd "$tiler_case_dir/../git-test-lib" && pwd)
export TEST_LIB_DIRECTORY
TEST_DIRECTORY=$tiler_case_dir
export TEST_DIRECTORY

. "$TEST_LIB_DIRECTORY/test-lib.sh"

# tiler_bin, hdiutil_bin, TILER, and MACOS come from sharness.d/tiler.sh.

if test "$(uname -s)" = Darwin && ! command -v "$hdiutil_bin" >/dev/null 2>&1
then
	skip_all="skipping macOS bake; $hdiutil_bin is missing"
	test_done
fi

src_dir=$HOME/src/app
work_dir=$HOME/work/bake
pub_dir=$HOME/work/published
image=$HOME/.tiler/images/alice/app/image.dmg
shadow=$HOME/.tiler/sessions/dev/runtime/m1/upper.shadow

if test -n "$tiler_bin"
then
	test_atexit '
		for _m in "$work_dir" "$pub_dir"
		do
			"$tiler_bin" umount "$_m" >/dev/null 2>&1 ||
			"$hdiutil_bin" detach "$_m" >/dev/null 2>&1 ||
			true
		done
	'
fi

test_expect_success TILER,MACOS 'a writable view is ready to bake' '
	mkdir -p "$src_dir" "$work_dir" &&
	echo "hello tiler" >"$src_dir/README.txt" &&
	"$tiler_bin" build --image alice/app "$src_dir" >build.out 2>build.err &&
	test_grep -E "^full alice/app [0-9a-f]+" build.out &&
	old_id=$(cut -d" " -f3 build.out) &&
	test -n "$old_id" &&
	test_path_is_file "$image" &&
	"$tiler_bin" session new dev >new.out 2>new.err &&
	"$tiler_bin" mount --session dev alice/app "$work_dir" >mount.out 2>mount.err &&
	test_grep -E "^mounted " mount.out &&
	echo baked >"$work_dir/KEEP.txt" &&
	test_path_is_file "$shadow" &&
	shadow_ino=$(stat -f %i "$shadow") &&
	test -n "$shadow_ino" &&
	"$tiler_bin" session show dev --json >before.json 2>before.err &&
	test_grep -F "\"full_id\": \"$old_id\"" before.json &&
	"$hdiutil_bin" info -plist >before.plist 2>before-info.err &&
	test_grep -F "$shadow" before.plist
'

test_expect_success TILER,MACOS 'baking publishes a new image and leaves the shadow' '
	echo immediate >"$work_dir/NOW.txt" &&
	(
		cd "$work_dir" &&
		"$tiler_bin" build --image alice/app
	) >bake.out 2>bake.err &&
	test_grep -F "dmg has no incremental layer" bake.err &&
	test_grep -E "^full alice/app [0-9a-f]+" bake.out &&
	new_id=$(cut -d" " -f3 bake.out) &&
	test -n "$new_id" &&
	test "$new_id" != "$old_id" &&
	test_path_is_file "$image" &&
	test_path_is_missing "${image%.dmg}.sqsh" &&
	test_path_is_file "$shadow" &&
	test "$(stat -f %i "$shadow")" = "$shadow_ino" &&
	test_path_is_file "$HOME/.tiler/images/alice/app/_hold/$old_id/image.dmg"
'

test_expect_success TILER,MACOS 'the published image contains the write from just before bake' '
	mkdir -p "$pub_dir" &&
	"$tiler_bin" session new reader >reader-new.out 2>reader-new.err &&
	test_grep -E "^created reader$" reader-new.out &&
	"$tiler_bin" mount --read-only --session reader alice/app "$pub_dir" >pub.out 2>pub.err &&
	test_grep -E "^mounted " pub.out &&
	test_grep immediate "$pub_dir/NOW.txt" &&
	test_grep baked "$pub_dir/KEEP.txt" &&
	test_path_is_missing "$HOME/.tiler/sessions/reader/runtime/m1/upper.shadow"
'

test_expect_success TILER,MACOS 'the live attach stays on the original shadow' '
	test_grep "hello tiler" "$work_dir/README.txt" &&
	test_grep baked "$work_dir/KEEP.txt" &&
	"$tiler_bin" session show dev --json >after.json 2>after.err &&
	test_grep -F "\"full_id\": \"$old_id\"" after.json &&
	test_grep ! -F "\"full_id\": \"$new_id\"" after.json &&
	"$hdiutil_bin" info -plist >after.plist 2>after-info.err &&
	test_grep -F "$shadow" after.plist &&
	work_phys=$(cd "$work_dir" && pwd -P) &&
	{ grep -F "$work_dir" after.plist || grep -F "$work_phys" after.plist; }
'

test_expect_success TILER,MACOS 'images show reports a single file and chain length 1' '
	"$tiler_bin" images show alice/app >show.out 2>show.err &&
	test_grep -F "$new_id" show.out &&
	test_grep -F "  form        single" show.out &&
	test_grep -F "  chain       1" show.out &&
	test_path_is_file "$image"
'

test_done
