#!/bin/sh
#
# tiler end-to-end test: build idempotency on macOS dmg.
#
# Real hdiutil embeds timestamps, so packing the same tree twice
# yields different image.dmg bytes. Build records a content_sha256
# of the source tree (create) or of the session upper.shadow after
# convert (bake) and skips hdiutil when that fingerprint matches the
# active image, so a repeat is idle: same id, unchanged meta.yaml,
# no archives or hold.
#
# The case covers three CLI shapes:
#
#   1. plain directory create,
#   2. writable-mount shadow bake,
#   3. subdirectory of that mount under a different --image
#      (directory create, not a shadow bake).
#
# The case lists only TILER,MACOS. Linux make e2e skips it. A Darwin
# host without hdiutil_bin skips the file instead of failing.

test_description='macOS dmg build is idempotent for create, bake, and divert'

tiler_case_dir=$(cd "$(dirname "$0")" && pwd)
TEST_LIB_DIRECTORY=$(cd "$tiler_case_dir/../git-test-lib" && pwd)
export TEST_LIB_DIRECTORY
TEST_DIRECTORY=$tiler_case_dir
export TEST_DIRECTORY

. "$TEST_LIB_DIRECTORY/test-lib.sh"

if test "$(uname -s)" = Darwin && ! command -v "$hdiutil_bin" >/dev/null 2>&1
then
	skip_all="skipping macOS build idempotency; $hdiutil_bin is missing"
	test_done
fi

src_dir=$HOME/src/app
work_dir=$HOME/work/app
alice_dir=$HOME/.tiler/images/alice/app
bob_dir=$HOME/.tiler/images/bob/docs
alice_meta=$alice_dir/meta.yaml
bob_meta=$bob_dir/meta.yaml
shadow=$HOME/.tiler/sessions/dev/runtime/m1/upper.shadow

if test -n "$tiler_bin"
then
	test_atexit '
		"$tiler_bin" umount "$work_dir" >/dev/null 2>&1 ||
		"$hdiutil_bin" detach "$work_dir" >/dev/null 2>&1 ||
		true
	'
fi

test_expect_success TILER,MACOS 'plain directory dmg create publishes alice/app' '
	mkdir -p "$src_dir/docs" &&
	echo "hello tiler" >"$src_dir/README.txt" &&
	echo "# Guide" >"$src_dir/docs/guide.md" &&
	"$tiler_bin" build --image alice/app "$src_dir" >full1.out 2>full1.err &&
	test_grep -E "^full alice/app [0-9a-f]+" full1.out &&
	id1=$(cut -d" " -f3 full1.out) &&
	test -n "$id1" &&
	alice_img=$alice_dir/_objects/$id1/image.dmg &&
	test_path_is_file "$alice_img" &&
	test_path_is_missing "${alice_img%.dmg}.sqsh" &&
	test_grep -E "^[[:space:]]+content_sha256: [0-9a-f]{64}$" "$alice_meta" &&
	cp "$alice_meta" full1.meta
'

test_expect_success TILER,MACOS 'plain directory dmg create is idempotent' '
	id1=$(cut -d" " -f3 full1.out) &&
	test -n "$id1" &&
	"$tiler_bin" build --image alice/app "$src_dir" >full2.out 2>full2.err &&
	test_grep -E "^full alice/app [0-9a-f]+" full2.out &&
	id2=$(cut -d" " -f3 full2.out) &&
	test "$id1" = "$id2" &&
	test_cmp full1.meta "$alice_meta"
'

test_expect_success TILER,MACOS 'shadow bake publishes a new full from the live view' '
	"$tiler_bin" session new dev >new.out 2>new.err &&
	test_grep -E "^created dev$" new.out &&
	mkdir -p "$work_dir" &&
	"$tiler_bin" mount --session dev alice/app "$work_dir" >mount.out 2>mount.err &&
	test_grep -E "^mounted " mount.out &&
	echo baked >"$work_dir/KEEP.txt" &&
	test_path_is_file "$shadow" &&
	shadow_ino=$(stat -f %i "$shadow") &&
	echo "$shadow_ino" >shadow.ino &&
	(
		cd "$work_dir" &&
		"$tiler_bin" build --image alice/app
	) >bake1.out 2>bake1.err &&
	test_grep -F "dmg has no incremental layer" bake1.err &&
	test_grep -E "^full alice/app [0-9a-f]+" bake1.out &&
	bake_id=$(cut -d" " -f3 bake1.out) &&
	test -n "$bake_id" &&
	alice_img=$alice_dir/_objects/$bake_id/image.dmg &&
	test_path_is_file "$alice_img" &&
	test_path_is_file "$alice_dir/_objects/$id1/image.dmg" &&
	test_grep -E "^[[:space:]]+content_sha256: [0-9a-f]{64}$" "$alice_meta" &&
	cp "$alice_meta" bake1.meta
'

test_expect_success TILER,MACOS 'shadow bake is idempotent without dropping the live shadow' '
	bake_id=$(cut -d" " -f3 bake1.out) &&
	test -n "$bake_id" &&
	shadow_ino=$(cat shadow.ino) &&
	(
		cd "$work_dir" &&
		"$tiler_bin" build --image alice/app
	) >bake2.out 2>bake2.err &&
	test_grep -E "^full alice/app [0-9a-f]+" bake2.out &&
	bake_id2=$(cut -d" " -f3 bake2.out) &&
	test "$bake_id" = "$bake_id2" &&
	test_cmp bake1.meta "$alice_meta" &&
	test_path_is_file "$shadow" &&
	test "$(stat -f %i "$shadow")" = "$shadow_ino"
'

test_expect_success TILER,MACOS 'subdir of a mount packs a different dmg without baking' '
	test_path_is_file "$work_dir/docs/guide.md" &&
	"$tiler_bin" build --image bob/docs "$work_dir/docs" >div1.out 2>div1.err &&
	test_grep -E "^full bob/docs [0-9a-f]+" div1.out &&
	test_grep ! -F "dmg has no incremental layer" div1.err &&
	bob_id=$(cut -d" " -f3 div1.out) &&
	test -n "$bob_id" &&
	bob_img=$bob_dir/_objects/$bob_id/image.dmg &&
	test_path_is_file "$bob_img" &&
	test_path_is_missing "${bob_img%.dmg}.sqsh" &&
	test_grep -E "^[[:space:]]+content_sha256: [0-9a-f]{64}$" "$bob_meta" &&
	cp "$bob_meta" div1.meta
'

test_expect_success TILER,MACOS 'subdir divert build is idempotent' '
	bob_id=$(cut -d" " -f3 div1.out) &&
	test -n "$bob_id" &&
	"$tiler_bin" build --image bob/docs "$work_dir/docs" >div2.out 2>div2.err &&
	test_grep -E "^full bob/docs [0-9a-f]+" div2.out &&
	bob_id2=$(cut -d" " -f3 div2.out) &&
	test "$bob_id" = "$bob_id2" &&
	test_cmp div1.meta "$bob_meta" &&
	test_path_is_file "$shadow"
'

test_done
