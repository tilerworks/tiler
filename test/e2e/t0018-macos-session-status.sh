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
# The MACOS and HDIUTIL prerequisites skip these tests on hosts without
# the macOS disk-image toolchain.

test_description='macOS session open reattaches a shadow and status is healthy'

. "$(dirname "$0")/lib-tiler.sh"

# tiler_bin, hdiutil_bin, TILER, and MACOS come from sharness.d/tiler.sh.

src_dir=$HOME/src/app
work_dir=$HOME/work/rec
img_dir=$HOME/.tiler/images/alice/app
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

test_expect_success TILER,MACOS,HDIUTIL 'a writable session mounts and status is healthy' '
	mkdir -p "$src_dir" "$work_dir" &&
	echo "hello tiler" >"$src_dir/README.txt" &&
	"$tiler_bin" build --image alice/app "$src_dir" >build.out 2>build.err &&
	test_grep -E "^full alice/app [0-9a-f]+" build.out &&
	id=$(cut -d" " -f3 build.out) &&
	test -n "$id" &&
	image=$img_dir/_objects/$id/image.dmg &&
	test_path_is_file "$image" &&
	"$tiler_bin" session new rec >new.out 2>new.err &&
	test_grep -E "^created rec$" new.out &&
	"$tiler_bin" mount --session rec alice/app "$work_dir" >mount.out 2>mount.err &&
	test_grep -E "^mounted " mount.out &&
	echo kept >"$work_dir/KEEP.txt" &&
	test_path_is_file "$shadow" &&
	shadow_ino=$(stat -f %i "$shadow") &&
	test -n "$shadow_ino" &&
	"$tiler_bin" status >live.out 2>live.err &&
	test_grep "\[rw · mounted · healthy\]" live.out &&
	test_grep "shadow.*upper.shadow" live.out
'

test_expect_success TILER,MACOS,HDIUTIL 'the mountpoint can disappear while the shadow stays' '
	if ! "$hdiutil_bin" detach "$work_dir" >detach.out 2>detach.err
	then
		phys=$(cd "$work_dir" && pwd -P) &&
		"$hdiutil_bin" detach "$phys" >detach.out 2>detach.err
	fi &&
	test_path_is_dir "$work_dir" &&
	test_path_is_missing "$work_dir/KEEP.txt" &&
	test_path_is_missing "$work_dir/README.txt" &&
	test_path_is_file "$shadow" &&
	current_shadow_ino=$(stat -f %i "$shadow") &&
	test "$current_shadow_ino" = "$shadow_ino" &&
	"$tiler_bin" status >stale.out 2>stale.err &&
	test_grep "\[rw · mounted · stale\]" stale.out
'

test_expect_success TILER,MACOS,HDIUTIL 'session open reattaches the same shadow' '
	"$tiler_bin" session open rec >open.out 2>open.err &&
	test_grep -E "^opened rec$" open.out &&
	test_grep "hello tiler" "$work_dir/README.txt" &&
	test_grep kept "$work_dir/KEEP.txt" &&
	test_path_is_file "$shadow" &&
	current_shadow_ino=$(stat -f %i "$shadow") &&
	test "$current_shadow_ino" = "$shadow_ino"
'

test_expect_success TILER,MACOS,HDIUTIL 'status is healthy when mountpoint, image, and shadow match' '
	"$tiler_bin" status >healthy.out 2>healthy.err &&
	test_grep "\[rw · mounted · healthy\]" healthy.out &&
	"$hdiutil_bin" info -plist >info.plist 2>info.err &&
	test_grep -F "$shadow" info.plist &&
	image_phys=$(cd "$(dirname "$image")" && pwd -P)/image.dmg &&
	test_grep -F -e "$image" -e "$image_phys" info.plist &&
	work_phys=$(cd "$work_dir" && pwd -P) &&
	test_grep -F -e "$work_dir" -e "$work_phys" info.plist
'

test_done
