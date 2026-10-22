#!/bin/sh
#
# tiler end-to-end test: session open reattaches a vanished dmg, and
# status reports healthy when the three paths match.
#
# The mount is detached with hdiutil, not tiler umount, so the record
# still says mounted while the mountpoint is gone. session open must
# attach the original upper.shadow again. status is healthy only when
# the mountpoint, the image path, and the shadow path agree.
#
# The case lists only TILER,MACOS. Linux make e2e skips it. A Darwin
# host without hdiutil_bin skips the file instead of failing.

test_description='macOS session open reattaches a shadow and status is healthy'

tiler_case_dir=$(cd "$(dirname "$0")" && pwd)
TEST_LIB_DIRECTORY=$(cd "$tiler_case_dir/../git-test-lib" && pwd)
export TEST_LIB_DIRECTORY
TEST_DIRECTORY=$tiler_case_dir
export TEST_DIRECTORY

. "$TEST_LIB_DIRECTORY/test-lib.sh"

# tiler_bin, hdiutil_bin, TILER, and MACOS come from sharness.d/tiler.sh.

if test "$(uname -s)" = Darwin && ! command -v "$hdiutil_bin" >/dev/null 2>&1
then
	skip_all="skipping macOS session status; $hdiutil_bin is missing"
	test_done
fi

src_dir=$HOME/src/app
work_dir=$HOME/work/rec
image=$HOME/.tiler/images/alice/app/image.dmg
shadow=$HOME/.tiler/sessions/rec/runtime/m1/upper.shadow

if test -n "$tiler_bin"
then
	test_atexit '
		for _m in "$work_dir"
		do
			"$tiler_bin" umount "$_m" >/dev/null 2>&1 ||
			"$hdiutil_bin" detach "$_m" >/dev/null 2>&1 ||
			true
		done
	'
fi

test_expect_success TILER,MACOS 'a writable session mounts and status is healthy' '
	mkdir -p "$src_dir" "$work_dir" &&
	echo "hello tiler" >"$src_dir/README.txt" &&
	"$tiler_bin" build --image alice/app "$src_dir" >build.out 2>build.err &&
	grep -Eq "^full alice/app [0-9a-f]+" build.out &&
	test -f "$image" &&
	"$tiler_bin" session new rec >new.out 2>new.err &&
	grep -Eq "^created rec$" new.out &&
	"$tiler_bin" mount --session rec alice/app "$work_dir" >mount.out 2>mount.err &&
	grep -Eq "^mounted " mount.out &&
	echo kept >"$work_dir/KEEP.txt" &&
	test -f "$shadow" &&
	shadow_ino=$(stat -f %i "$shadow") &&
	test -n "$shadow_ino" &&
	"$tiler_bin" status >live.out 2>live.err &&
	grep -F "$work_dir" live.out | grep -q "health=healthy"
'

test_expect_success TILER,MACOS 'the mountpoint can disappear while the shadow stays' '
	if ! "$hdiutil_bin" detach "$work_dir" >detach.out 2>detach.err
	then
		phys=$(cd "$work_dir" && pwd -P) &&
		"$hdiutil_bin" detach "$phys" >detach.out 2>detach.err
	fi &&
	test -d "$work_dir" &&
	test ! -e "$work_dir/KEEP.txt" &&
	test ! -e "$work_dir/README.txt" &&
	test -f "$shadow" &&
	test "$(stat -f %i "$shadow")" = "$shadow_ino" &&
	"$tiler_bin" status >stale.out 2>stale.err &&
	grep -F "$work_dir" stale.out | grep -q "health=stale"
'

test_expect_success TILER,MACOS 'session open reattaches the same shadow' '
	"$tiler_bin" session open rec >open.out 2>open.err &&
	grep -Eq "^opened rec$" open.out &&
	grep -q "hello tiler" "$work_dir/README.txt" &&
	grep -q kept "$work_dir/KEEP.txt" &&
	test -f "$shadow" &&
	test "$(stat -f %i "$shadow")" = "$shadow_ino"
'

test_expect_success TILER,MACOS 'status is healthy when mountpoint, image, and shadow match' '
	"$tiler_bin" status >healthy.out 2>healthy.err &&
	grep -F "$work_dir" healthy.out | grep -q "health=healthy" &&
	"$hdiutil_bin" info -plist >info.plist 2>info.err &&
	grep -F "$shadow" info.plist &&
	image_phys=$(cd "$(dirname "$image")" && pwd -P)/image.dmg &&
	{ grep -F "$image" info.plist || grep -F "$image_phys" info.plist; } &&
	work_phys=$(cd "$work_dir" && pwd -P) &&
	{ grep -F "$work_dir" info.plist || grep -F "$work_phys" info.plist; }
'

test_done
