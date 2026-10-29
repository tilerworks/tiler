#!/bin/sh
#
# tiler end-to-end test: link an external squashfs, then mount by name.
#
# Companion to t0013-mount-file-path.sh (bare path mount stays out of the
# library). This case covers the NAS link form:
#
#   1. pack a bare squashfs outside images_dir,
#   2. build --link writes a position file and meta.yaml; mount by name,
#   3. repeat build --link is a no-op and the still-mounted view stays up,
#   4. confirm the view is writable and build --incremental appends a layer,
#   5. build --link with a dmg (fake koly) is refused on Linux (exit 1),
#   6. umount, then an ordinary mount of the position file serves content.
#
# A real mount needs a writable /dev/fuse plus the squashfs and overlay
# toolchain that `tiler doctor` treats as fail-level. FUSE_TOOLCHAIN from
# sharness.d/tiler.sh models exactly that, so the case skips cleanly --
# rather than false-fails -- on a host without the toolchain.
#
# Scope guard: this case asserts link import, library mount,
# incremental build, link idempotency, position-file remount, and
# Linux dmg refusal only. It does not cover macOS dmg link (t0024),
# bare path mounts, split blocks, or images remove/gc against NAS files.

test_description='build --link imports a NAS squashfs and ordinary mount supports incr'

# Resolve paths against this script's location BEFORE sourcing the
# framework: test-lib.sh moves the working directory into a per-run
# trash directory and repoints HOME there.
tiler_case_dir=$(cd "$(dirname "$0")" && pwd)
TEST_LIB_DIRECTORY=$(cd "$tiler_case_dir/../git-test-lib" && pwd)
export TEST_LIB_DIRECTORY
TEST_DIRECTORY=$tiler_case_dir
export TEST_DIRECTORY

. "$TEST_LIB_DIRECTORY/test-lib.sh"

# tiler_bin, TILER, LINUX, and FUSE_TOOLCHAIN come from sharness.d/tiler.sh.

# HOME is the per-run trash directory, so tiler's default layout
# ($HOME/.tiler/{images,sessions,cache}) is already sandboxed.
# The third-party squashfs lives under $HOME/nas/ so it is outside
# images_dir; mount targets stay under $HOME/work.
src_dir=$HOME/src/vendor
nas_dir=$HOME/nas
img=$nas_dir/vendor.sqsh
lib_name=nas/vendor
work_dir=$HOME/work/vendor
img_dir=$HOME/.tiler/images/nas/vendor
meta_file=$img_dir/meta.yaml
images_dir=$HOME/.tiler/images

# Guarantee every FUSE mount is torn down before the framework removes
# the trash directory, even if an assertion fails or the run is
# interrupted. `tiler umount` is idempotent; fusermount3 -uz is the
# fallback for a view whose daemon already died.
if test -n "$tiler_bin"
then
	test_atexit '
		"$tiler_bin" umount "$work_dir" >/dev/null 2>&1 ||
		"$fusermount3_bin" -uz "$work_dir" >/dev/null 2>&1 ||
		true
	'
fi

# git-test-lib eval's each body in the current shell, so base_id/layer
# captured below persist across the later test_expect_success blocks.

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'pack a bare squashfs outside images_dir' '
	mkdir -p "$src_dir/docs" "$nas_dir" &&
	echo "hello vendor" >"$src_dir/README.txt" &&
	echo "# guide" >"$src_dir/docs/guide.md" &&
	mksquashfs "$src_dir" "$img" -comp zstd -noappend -xattrs >/dev/null &&
	test_path_is_file "$img" &&
	case "$img" in
	"$images_dir"/*)
		echo "image path must stay outside images_dir: $img" >&2
		false
		;;
	esac &&
	test_path_is_missing "$img_dir/meta.yaml"
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'build --link records a position file and mount serves content' '
	mkdir -p "$work_dir" &&
	"$tiler_bin" build --link "$img" --image "$lib_name" >link.out 2>link.err &&
	test_grep -E "^link $lib_name [0-9a-f]+$" link.out &&
	base_id=$(cut -d" " -f3 link.out) &&
	test -n "$base_id" &&
	"$tiler_bin" mount "$lib_name" "$work_dir" >mount.out 2>mount.err &&
	test_grep -E "^new session created: [a-z]+-[a-z]+[0-9]?$" mount.out &&
	test_grep -E "^mounted " mount.out &&
	test_path_is_file "$meta_file" &&
	test "$(grep -c "^[[:space:]]*- id:" "$meta_file")" = 1 &&
	payload=$img_dir/_objects/$base_id/image.sqsh &&
	test_path_is_file "$payload" &&
	! test -L "$payload" &&
	test "$(cat "$payload")" = "$img" &&
	test_grep "hello vendor" "$work_dir/README.txt" &&
	test_path_is_file "$work_dir/docs/guide.md" &&
	auto=$(sed -n "s/^new session created: //p" mount.out) &&
	test -n "$auto" &&
	"$tiler_bin" session show "$auto" >show.out 2>show.err &&
	test_grep -E "^    1  mount  $lib_name  .*  mode=rw  status=mounted" show.out &&
	"$tiler_bin" session show "$auto" --json >showj.out 2>showj.err &&
	test_grep -F "\"image\": \"$lib_name\"" showj.out &&
	test_grep -F "\"full_id\": \"$base_id\"" showj.out
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'repeat build --link leaves meta unchanged' '
	cp "$meta_file" meta.before &&
	"$tiler_bin" build --link "$img" --image "$lib_name" >link2.out 2>link2.err &&
	test_grep -E "^no-op $lib_name $base_id$" link2.out &&
	test_cmp meta.before "$meta_file" &&
	"$tiler_bin" mount "$lib_name" "$work_dir" >link2mount.out 2>link2mount.err &&
	test_grep -E "^already mounted " link2mount.out
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'writable edit and build --incremental grow the chain' '
	echo from-incr >"$work_dir/extra.txt" &&
	test_path_is_file "$work_dir/extra.txt" &&
	( cd "$work_dir" && "$tiler_bin" build --incremental ) >incr.out 2>incr.err &&
	test_grep -E "^incr $lib_name [0-9a-f]+" incr.out &&
	layer=$(cut -d" " -f3 incr.out) &&
	test -n "$layer" &&
	test "$layer" != "$base_id" &&
	test_path_is_file "$payload" &&
	test_path_is_file "$img_dir/_objects/$layer/image.sqsh" &&
	test_grep -E "^[[:space:]]*- id: \"?$base_id\"?$" "$meta_file" &&
	"$tiler_bin" images show "$lib_name" >show.out 2>show.err &&
	test_grep -E "^  parent +$base_id$" show.out
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'build --link refuses a dmg source on Linux' '
	dmg=$nas_dir/fake.dmg &&
	printf koly >"$dmg" &&
	dd if=/dev/zero bs=508 count=1 >>"$dmg" 2>/dev/null &&
	test_path_is_file "$dmg" &&
	test_expect_code 1 "$tiler_bin" build --link "$dmg" --image nas/disk >dmg.out 2>dmg.err &&
	test_grep dmg dmg.err &&
	test_path_is_missing "$images_dir/nas/disk"
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'umount tears the view down' '
	"$tiler_bin" umount "$work_dir" >umount.out 2>umount.err &&
	test_grep -E "^unmounted " umount.out
'

# The link import already stored a position file: one absolute path
# and a newline, never a symlink. Remount by library name.
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'position file mounts by library name' '
	test_path_is_file "$payload" &&
	! test -L "$payload" &&
	mkdir -p "$work_dir" &&
	"$tiler_bin" mount "$lib_name" "$work_dir" >pathref.out 2>pathref.err &&
	test_grep -E "^mounted " pathref.out &&
	test_grep "hello vendor" "$work_dir/README.txt" &&
	"$tiler_bin" umount "$work_dir" >pathref-umount.out 2>pathref-umount.err &&
	test_grep -E "^unmounted " pathref-umount.out
'

test_done
