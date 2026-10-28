#!/bin/sh
#
# tiler end-to-end test: session open remounts a pin after build --full.
#
# Companion to t0004-image-incr-cascade.sh (full merge while mounted,
# remount of the new active) and t0007-session-recovery.sh (open after
# crash without replacing the library image). Neither covers this case:
# after build --full moves the pinned id into _hold/, umount leaves the
# session active with its upper (or shadow), and session open must
# reattach that held lower — not the new active — so the overlay still
# merges old base content with the pre-rebuild upper write.
#
# Linux (TILER,LINUX,FUSE_TOOLCHAIN): single-file squashfs + overlay.
# macOS (TILER,MACOS): single-file dmg + upper.shadow, same sequence.
# Blocks --split stays in unit/CLI coverage from earlier steps.
#
# A Darwin host without hdiutil skips the whole file (Linux prereqs
# would not run there either). A Linux host without FUSE skips only
# the Linux blocks; macOS blocks are skipped via the MACOS prereq.

test_description='session open remounts a held pin after build --full'

tiler_case_dir=$(cd "$(dirname "$0")" && pwd)
TEST_LIB_DIRECTORY=$(cd "$tiler_case_dir/../git-test-lib" && pwd)
export TEST_LIB_DIRECTORY
TEST_DIRECTORY=$tiler_case_dir
export TEST_DIRECTORY

. "$TEST_LIB_DIRECTORY/test-lib.sh"

# tiler_bin, hdiutil_bin, fusermount3_bin, TILER, LINUX, MACOS, and
# FUSE_TOOLCHAIN come from sharness.d/tiler.sh.

if test "$(uname -s)" = Darwin && ! command -v "$hdiutil_bin" >/dev/null 2>&1
then
	skip_all="skipping session-open-after-full; $hdiutil_bin is missing"
	test_done
fi

src_dir=$HOME/src/app
work_dir=$HOME/work/pin
img_dir=$HOME/.tiler/images/alice/app
runtime_dir=$HOME/.tiler/sessions/pin/runtime/m1
upper_dir=$runtime_dir/upper
shadow=$runtime_dir/upper.shadow

if test -n "$tiler_bin"
then
	test_atexit '
		for _m in "$work_dir"
		do
			"$tiler_bin" umount "$_m" >/dev/null 2>&1 ||
			"$fusermount3_bin" -uz "$_m" >/dev/null 2>&1 ||
			"$hdiutil_bin" detach "$_m" >/dev/null 2>&1 ||
			true
		done
	'
fi

# --- Linux: squashfs + overlay ---

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'linux: pin a writable view and write the upper' '
	mkdir -p "$src_dir" "$work_dir" &&
	echo "hello tiler" >"$src_dir/README.txt" &&
	"$tiler_bin" build --image alice/app "$src_dir" >build.out 2>build.err &&
	test_grep -E "^full alice/app [0-9a-f]+" build.out &&
	old_id=$(cut -d" " -f3 build.out) &&
	test -n "$old_id" &&
	"$tiler_bin" session new pin >new.out 2>new.err &&
	test_grep -E "^created pin$" new.out &&
	"$tiler_bin" mount --session pin alice/app "$work_dir" >mount.out 2>mount.err &&
	test_grep -E "^mounted " mount.out &&
	test_grep "hello tiler" "$work_dir/README.txt" &&
	echo from-upper >"$work_dir/from-upper.txt" &&
	"$tiler_bin" session show pin --json >before.json 2>before.err &&
	test_grep -F "\"full_id\": \"$old_id\"" before.json &&
	test_path_is_file "$upper_dir/from-upper.txt"
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'linux: build --full holds the pin and changes active' '
	old_id=$(cut -d" " -f3 build.out) &&
	echo only-in-new >"$src_dir/only-in-new.txt" &&
	"$tiler_bin" build --full --image alice/app "$src_dir" >full.out 2>full.err &&
	test_grep -E "^full alice/app [0-9a-f]+" full.out &&
	new_id=$(cut -d" " -f3 full.out) &&
	test -n "$new_id" &&
	test "$new_id" != "$old_id" &&
	test_path_is_file "$img_dir/_hold/$old_id/image.sqsh" &&
	test_grep -E "^head: \"?$new_id\"?$" "$img_dir/meta.yaml"
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'linux: umount keeps the session and upper' '
	old_id=$(cut -d" " -f3 build.out) &&
	"$tiler_bin" umount "$work_dir" >umount.out 2>umount.err &&
	test_grep -E "^unmounted " umount.out &&
	"$tiler_bin" session list >list.out 2>list.err &&
	test_grep -E "^pin[[:space:]]+active[[:space:]]+" list.out &&
	test_path_is_dir "$runtime_dir" &&
	test_path_is_file "$upper_dir/from-upper.txt" &&
	"$tiler_bin" session show pin --json >after-umount.json 2>after-umount.err &&
	test_grep -F "\"full_id\": \"$old_id\"" after-umount.json
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'linux: session open merges held lower with upper' '
	old_id=$(cut -d" " -f3 build.out) &&
	new_id=$(cut -d" " -f3 full.out) &&
	"$tiler_bin" session open pin >open.out 2>open.err &&
	test_grep -E "^opened pin$" open.out &&
	test_grep "hello tiler" "$work_dir/README.txt" &&
	test_grep from-upper "$work_dir/from-upper.txt" &&
	test_path_is_missing "$work_dir/only-in-new.txt" &&
	"$tiler_bin" session show pin --json >after-open.json 2>after-open.err &&
	test_grep -F "\"full_id\": \"$old_id\"" after-open.json &&
	test_grep ! -F "\"full_id\": \"$new_id\"" after-open.json
'

# --- macOS: dmg + upper.shadow ---

test_expect_success TILER,MACOS 'macos: pin a writable dmg view and write the shadow' '
	mkdir -p "$src_dir" "$work_dir" &&
	echo "hello tiler" >"$src_dir/README.txt" &&
	rm -f "$src_dir/only-in-new.txt" &&
	"$tiler_bin" build --image alice/app "$src_dir" >build.out 2>build.err &&
	test_grep -E "^full alice/app [0-9a-f]+" build.out &&
	old_id=$(cut -d" " -f3 build.out) &&
	test -n "$old_id" &&
	"$tiler_bin" session new pin >new.out 2>new.err &&
	test_grep -E "^created pin$" new.out &&
	"$tiler_bin" mount --session pin alice/app "$work_dir" >mount.out 2>mount.err &&
	test_grep -E "^mounted " mount.out &&
	test_grep "hello tiler" "$work_dir/README.txt" &&
	echo from-upper >"$work_dir/from-upper.txt" &&
	test_path_is_file "$shadow" &&
	shadow_ino=$(stat -f %i "$shadow") &&
	test -n "$shadow_ino" &&
	"$tiler_bin" session show pin --json >before.json 2>before.err &&
	test_grep -F "\"full_id\": \"$old_id\"" before.json
'

test_expect_success TILER,MACOS 'macos: build --full holds the pin and changes active' '
	old_id=$(cut -d" " -f3 build.out) &&
	echo only-in-new >"$src_dir/only-in-new.txt" &&
	"$tiler_bin" build --full --image alice/app "$src_dir" >full.out 2>full.err &&
	test_grep -E "^full alice/app [0-9a-f]+" full.out &&
	new_id=$(cut -d" " -f3 full.out) &&
	test -n "$new_id" &&
	test "$new_id" != "$old_id" &&
	test_path_is_file "$img_dir/_hold/$old_id/image.dmg" &&
	test_grep -E "^head: \"?$new_id\"?$" "$img_dir/meta.yaml"
'

test_expect_success TILER,MACOS 'macos: umount keeps the session and shadow' '
	old_id=$(cut -d" " -f3 build.out) &&
	"$tiler_bin" umount "$work_dir" >umount.out 2>umount.err &&
	test_grep -E "^unmounted " umount.out &&
	"$tiler_bin" session list >list.out 2>list.err &&
	test_grep -E "^pin[[:space:]]+active[[:space:]]+" list.out &&
	test_path_is_file "$shadow" &&
	test "$(stat -f %i "$shadow")" = "$shadow_ino" &&
	"$tiler_bin" session show pin --json >after-umount.json 2>after-umount.err &&
	test_grep -F "\"full_id\": \"$old_id\"" after-umount.json
'

test_expect_success TILER,MACOS 'macos: session open attaches held dmg with the shadow' '
	old_id=$(cut -d" " -f3 build.out) &&
	new_id=$(cut -d" " -f3 full.out) &&
	"$tiler_bin" session open pin >open.out 2>open.err &&
	test_grep -E "^opened pin$" open.out &&
	test_grep "hello tiler" "$work_dir/README.txt" &&
	test_grep from-upper "$work_dir/from-upper.txt" &&
	test_path_is_missing "$work_dir/only-in-new.txt" &&
	test_path_is_file "$shadow" &&
	test "$(stat -f %i "$shadow")" = "$shadow_ino" &&
	"$hdiutil_bin" info -plist >open.plist 2>open-info.err &&
	test_grep -F "$shadow" open.plist &&
	"$tiler_bin" session show pin --json >after-open.json 2>after-open.err &&
	test_grep -F "\"full_id\": \"$old_id\"" after-open.json &&
	test_grep ! -F "\"full_id\": \"$new_id\"" after-open.json
'

test_done
