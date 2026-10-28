#!/bin/sh
#
# tiler end-to-end test: register an external dmg with mount --file.
#
# Companion to t0023-mount-file-link.sh (Linux squashfs link). On macOS:
#
#   1. build a dmg outside images_dir,
#   2. mount --file registers a payload reference and mounts by name,
#   3. a writable mount writes through upper.shadow,
#   4. full bake materializes image.dmg while the NAS file stays put,
#   5. mount --file with squashfs magic is refused (exit 2).
#
# The case lists only TILER,MACOS. Linux make e2e skips it. A Darwin
# host without hdiutil_bin skips the file instead of failing.

test_description='macOS mount --file links external dmg and refuses squashfs'

tiler_case_dir=$(cd "$(dirname "$0")" && pwd)
TEST_LIB_DIRECTORY=$(cd "$tiler_case_dir/../git-test-lib" && pwd)
export TEST_LIB_DIRECTORY
TEST_DIRECTORY=$tiler_case_dir
export TEST_DIRECTORY

. "$TEST_LIB_DIRECTORY/test-lib.sh"

# tiler_bin, hdiutil_bin, TILER, and MACOS come from sharness.d/tiler.sh.

if test "$(uname -s)" = Darwin && ! command -v "$hdiutil_bin" >/dev/null 2>&1
then
	skip_all="skipping macOS mount --file link; $hdiutil_bin is missing"
	test_done
fi

# HOME is the per-run trash directory, so the default layout under
# $HOME/.tiler is already sandboxed. The external dmg lives under
# $HOME/nas/; mount targets stay under $HOME/work.
src_dir=$HOME/src/vendor
nas_dir=$HOME/nas
dmg=$nas_dir/vendor.dmg
lib_name=nas/vendor
work_dir=$HOME/work/vendor
pub_dir=$HOME/work/published
sqsh_try_dir=$HOME/work/sqsh-refuse
img_dir=$HOME/.tiler/images/nas/vendor
meta_file=$img_dir/meta.yaml
shadow=$HOME/.tiler/sessions/dev/runtime/m1/upper.shadow
images_dir=$HOME/.tiler/images

if test -n "$tiler_bin"
then
	test_atexit '
		for _m in "$work_dir" "$pub_dir" "$sqsh_try_dir"
		do
			"$tiler_bin" umount "$_m" >/dev/null 2>&1 ||
			"$hdiutil_bin" detach "$_m" >/dev/null 2>&1 ||
			true
		done
	'
fi

# git-test-lib eval's each body in the current shell, so base_id and
# shadow_ino captured below persist across the later blocks.

test_expect_success TILER,MACOS 'build a dmg outside images_dir' '
	mkdir -p "$src_dir" "$nas_dir" &&
	echo "hello vendor" >"$src_dir/README.txt" &&
	"$tiler_bin" build --image scratch/vendor "$src_dir" >build.out 2>build.err &&
	test_grep -E "^full scratch/vendor [0-9a-f]+" build.out &&
	staged_id=$(cut -d" " -f3 build.out) &&
	test -n "$staged_id" &&
	staged=$HOME/.tiler/images/scratch/vendor/_objects/$staged_id/image.dmg &&
	test_path_is_file "$staged" &&
	mv "$staged" "$dmg" &&
	rm -rf "$HOME/.tiler/images/scratch" &&
	test_path_is_file "$dmg" &&
	test_path_is_missing "$img_dir/meta.yaml" &&
	case "$dmg" in
	"$images_dir"/*)
		echo "dmg must stay outside images_dir: $dmg" >&2
		false
		;;
	esac
'

test_expect_success TILER,MACOS 'mount --file registers a payload reference and mounts with shadow' '
	mkdir -p "$work_dir" &&
	"$tiler_bin" session new dev >new.out 2>new.err &&
	test_grep -E "^created dev$" new.out &&
	"$tiler_bin" mount --session dev --file "$dmg" "$lib_name" "$work_dir" >mount.out 2>mount.err &&
	test_grep -E "^mounted " mount.out &&
	test_path_is_file "$meta_file" &&
	test "$(grep -c "^[[:space:]]*- id:" "$meta_file")" = 1 &&
	base_id=$(sed -n "s/^[[:space:]]*- id: //p" "$meta_file") &&
	test -n "$base_id" &&
	payload=$img_dir/_objects/$base_id/image.dmg &&
	test_path_is_file "$payload" &&
	test "$(cat "$payload")" = "$dmg" &&
	test_grep "hello vendor" "$work_dir/README.txt" &&
	echo linked >"$work_dir/LINK.txt" &&
	test_path_is_file "$shadow" &&
	shadow_ino=$(stat -f %i "$shadow") &&
	test -n "$shadow_ino" &&
	"$tiler_bin" session show dev --json >showj.out 2>showj.err &&
	test_grep -F "\"image\": \"$lib_name\"" showj.out &&
	test_grep -F "\"full_id\": \"$base_id\"" showj.out
'

test_expect_success TILER,MACOS 'full bake materializes the active dmg' '
	echo baked >"$work_dir/BAKE.txt" &&
	(
		cd "$work_dir" &&
		"$tiler_bin" build --image "$lib_name"
	) >bake.out 2>bake.err &&
	test_grep -F "dmg has no incremental layer" bake.err &&
	test_grep -E "^full $lib_name [0-9a-f]+" bake.out &&
	new_id=$(cut -d" " -f3 bake.out) &&
	test -n "$new_id" &&
	test "$new_id" != "$base_id" &&
	test_path_is_file "$payload" &&
	test "$(cat "$payload")" = "$dmg" &&
	payload=$img_dir/_objects/$new_id/image.dmg &&
	test_path_is_file "$payload" &&
	test ! -L "$payload" &&
	test_path_is_file "$shadow" &&
	test "$(stat -f %i "$shadow")" = "$shadow_ino" &&
	test_path_is_file "$dmg"
'

test_expect_success TILER,MACOS 'read-only mount of baked image has the writes' '
	mkdir -p "$pub_dir" &&
	"$tiler_bin" session new reader >reader-new.out 2>reader-new.err &&
	test_grep -E "^created reader$" reader-new.out &&
	"$tiler_bin" mount --read-only --session reader "$lib_name" "$pub_dir" >pub.out 2>pub.err &&
	test_grep -E "^mounted " pub.out &&
	test_grep linked "$pub_dir/LINK.txt" &&
	test_grep baked "$pub_dir/BAKE.txt" &&
	test_path_is_missing "$HOME/.tiler/sessions/reader/runtime/m1/upper.shadow"
'

test_expect_success TILER,MACOS 'mount --file refuses squashfs on macOS' '
	sqsh=$nas_dir/vendor.sqsh &&
	printf hsqs-fake >"$sqsh" &&
	mkdir -p "$sqsh_try_dir" &&
	test_expect_code 2 "$tiler_bin" mount --file "$sqsh" nas/sqsh "$sqsh_try_dir" >sq.out 2>sq.err &&
	test_grep "not available on macOS" sq.err
'

test_expect_success TILER,MACOS 'umount tears the writable view down' '
	"$tiler_bin" umount "$work_dir" >umount.out 2>umount.err &&
	test_grep -E "^unmounted " umount.out
'

test_done
