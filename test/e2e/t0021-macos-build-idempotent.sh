#!/bin/sh
#
# tiler end-to-end test: build repeatability on macOS dmg.
#
# Squashfs pins SOURCE_DATE_EPOCH so an identical tree packs to the
# same bytes and publish is idle (see t0020). Real hdiutil embeds
# timestamps, so a second create or bake of the same inputs often
# gets a new digest and archives the previous image. Deterministic
# idle publish for dmg is covered by unit tests that inject a stable
# hdiutil. This case still drives the three CLI shapes twice:
#
#   1. plain directory create,
#   2. writable-mount shadow bake,
#   3. subdirectory of that mount under a different --image
#      (directory create, not a shadow bake).
#
# When the digest is stable the meta must stay put. When hdiutil
# changes the bytes, the previous id must rest in archives or hold.
#
# The case lists only TILER,MACOS. Linux make e2e skips it. A Darwin
# host without hdiutil_bin skips the file instead of failing.

test_description='macOS dmg build repeats create, bake, and diverted subdir'

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
alice_img=$alice_dir/image.dmg
bob_img=$bob_dir/image.dmg
shadow=$HOME/.tiler/sessions/dev/runtime/m1/upper.shadow

# assert_idle_or_archived META_COPY ID REPO_DIR
# Idle: same id and unchanged meta, no new parking required.
# Republish: previous id rests under archives/ or hold/.
assert_idle_or_archived() {
	_meta_copy=$1
	_old_id=$2
	_repo=$3
	_new_id=$4
	if test "$_old_id" = "$_new_id"
	then
		cmp -s "$_meta_copy" "$_repo/meta.yaml"
	else
		test -d "$_repo/archives/$_old_id" ||
		test -d "$_repo/hold/$_old_id"
	fi
}

if test -n "$tiler_bin"
then
	test_atexit '
		"$tiler_bin" umount "$work_dir" >/dev/null 2>&1 ||
		"$hdiutil_bin" detach "$work_dir" >/dev/null 2>&1 ||
		true
	'
fi

test_expect_success TILER,MACOS 'plain directory dmg create repeats cleanly' '
	mkdir -p "$src_dir/docs" &&
	echo "hello tiler" >"$src_dir/README.txt" &&
	echo "# Guide" >"$src_dir/docs/guide.md" &&
	"$tiler_bin" build --image alice/app "$src_dir" >full1.out 2>full1.err &&
	grep -Eq "^full alice/app [0-9a-f]+" full1.out &&
	id1=$(cut -d" " -f3 full1.out) &&
	test -n "$id1" &&
	test -f "$alice_img" &&
	test ! -e "${alice_img%.dmg}.sqsh" &&
	cp "$alice_meta" full1.meta &&
	"$tiler_bin" build --image alice/app "$src_dir" >full2.out 2>full2.err &&
	grep -Eq "^full alice/app [0-9a-f]+" full2.out &&
	id2=$(cut -d" " -f3 full2.out) &&
	test -n "$id2" &&
	assert_idle_or_archived full1.meta "$id1" "$alice_dir" "$id2"
'

test_expect_success TILER,MACOS 'shadow bake repeats without dropping the live shadow' '
	"$tiler_bin" session new dev >new.out 2>new.err &&
	grep -Eq "^created dev$" new.out &&
	mkdir -p "$work_dir" &&
	"$tiler_bin" mount --session dev alice/app "$work_dir" >mount.out 2>mount.err &&
	grep -Eq "^mounted " mount.out &&
	echo baked >"$work_dir/KEEP.txt" &&
	test -f "$shadow" &&
	shadow_ino=$(stat -f %i "$shadow") &&
	(
		cd "$work_dir" &&
		"$tiler_bin" build --image alice/app
	) >bake1.out 2>bake1.err &&
	grep -F "dmg has no incremental layer" bake1.err &&
	grep -Eq "^full alice/app [0-9a-f]+" bake1.out &&
	bake_id=$(cut -d" " -f3 bake1.out) &&
	test -n "$bake_id" &&
	cp "$alice_meta" bake1.meta &&
	(
		cd "$work_dir" &&
		"$tiler_bin" build --image alice/app
	) >bake2.out 2>bake2.err &&
	grep -Eq "^full alice/app [0-9a-f]+" bake2.out &&
	bake_id2=$(cut -d" " -f3 bake2.out) &&
	test -n "$bake_id2" &&
	assert_idle_or_archived bake1.meta "$bake_id" "$alice_dir" "$bake_id2" &&
	test -f "$shadow" &&
	test "$(stat -f %i "$shadow")" = "$shadow_ino"
'

test_expect_success TILER,MACOS 'subdir of a mount packs a different dmg without baking' '
	test -f "$work_dir/docs/guide.md" &&
	"$tiler_bin" build --image bob/docs "$work_dir/docs" >div1.out 2>div1.err &&
	grep -Eq "^full bob/docs [0-9a-f]+" div1.out &&
	! grep -F "dmg has no incremental layer" div1.err &&
	bob_id=$(cut -d" " -f3 div1.out) &&
	test -n "$bob_id" &&
	test -f "$bob_img" &&
	test ! -e "${bob_img%.dmg}.sqsh" &&
	cp "$bob_meta" div1.meta &&
	"$tiler_bin" build --image bob/docs "$work_dir/docs" >div2.out 2>div2.err &&
	grep -Eq "^full bob/docs [0-9a-f]+" div2.out &&
	bob_id2=$(cut -d" " -f3 div2.out) &&
	test -n "$bob_id2" &&
	assert_idle_or_archived div1.meta "$bob_id" "$bob_dir" "$bob_id2" &&
	test -f "$shadow"
'

test_done
