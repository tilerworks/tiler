#!/bin/sh
#
# tiler end-to-end test: register an external squashfs with mount --file.
#
# Companion to t0013-mount-file-path.sh (bare path mount stays out of the
# library). This case covers the NAS link form:
#
#   1. pack a bare squashfs outside images_dir,
#   2. mount --file registers symlink + meta.yaml and mounts by library name,
#   3. repeat mount --file with the same args is idempotent for the leaf,
#   4. confirm the view is writable and build --incremental appends _incr,
#   5. mount --file with a dmg (fake koly) is refused on Linux (exit 2).
#
# A real mount needs a writable /dev/fuse plus the squashfs and overlay
# toolchain that `tiler doctor` treats as fail-level. FUSE_TOOLCHAIN from
# sharness.d/tiler.sh models exactly that, so the case skips cleanly --
# rather than false-fails -- on a host without the toolchain.
#
# Scope guard: this case asserts --file registration, library mount,
# incremental build, link idempotency, path-ref remount (git-style
# text leaf), and Linux dmg refusal only. It does not cover macOS
# dmg --file (t0024), bare path mounts, split blocks, or images
# remove/gc against NAS files.

test_description='tiler mount --file links NAS squashfs and supports incr'

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
dmg_try_dir=$HOME/work/dmg-refuse
img_dir=$HOME/.tiler/images/nas/vendor
meta_file=$img_dir/meta.yaml
payload=$img_dir/image.sqsh
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

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'mount --file registers symlink meta and serves content' '
	mkdir -p "$work_dir" &&
	"$tiler_bin" mount --file "$img" "$lib_name" "$work_dir" >mount.out 2>mount.err &&
	test_grep -E "^new session created: [a-z]+-[a-z]+[0-9]?$" mount.out &&
	test_grep -E "^mounted " mount.out &&
	test_path_is_symlink "$payload" &&
	test "$(readlink "$payload")" = "$img" &&
	test_path_is_file "$meta_file" &&
	test_grep -E "^id: \"?[0-9a-f]+\"?$" "$meta_file" &&
	test_grep ! "^chain:" "$meta_file" &&
	base_id=$(sed -n "s/^id: \"\\?\([0-9a-f]*\\)\"\\?$/\\1/p" "$meta_file") &&
	test -n "$base_id" &&
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

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'repeat mount --file leaves meta unchanged' '
	cp "$meta_file" meta.before &&
	"$tiler_bin" mount --file "$img" "$lib_name" "$work_dir" >link2.out 2>link2.err &&
	test_grep -E "^already mounted " link2.out &&
	test_cmp meta.before "$meta_file"
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'writable edit and build --incremental grow the chain' '
	echo from-incr >"$work_dir/extra.txt" &&
	test_path_is_file "$work_dir/extra.txt" &&
	( cd "$work_dir" && "$tiler_bin" build --incremental ) >incr.out 2>incr.err &&
	test_grep -E "^incr $lib_name [0-9a-f]+" incr.out &&
	layer=$(cut -d" " -f3 incr.out) &&
	test -n "$layer" &&
	test "$layer" != "$base_id" &&
	test_path_is_symlink "$payload" &&
	test_path_is_file "$img_dir/_incr/$layer.sqsh" &&
	test_grep -E "^id: \"?$base_id\"?$" "$meta_file" &&
	"$tiler_bin" images show "$lib_name" >show.out 2>show.err &&
	test_grep -E "^  chain +1$" show.out
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'mount --file refuses a dmg source on Linux' '
	dmg=$nas_dir/fake.dmg &&
	printf koly >"$dmg" &&
	dd if=/dev/zero bs=508 count=1 >>"$dmg" 2>/dev/null &&
	test_path_is_file "$dmg" &&
	mkdir -p "$dmg_try_dir" &&
	test_expect_code 2 "$tiler_bin" mount --file "$dmg" nas/disk "$dmg_try_dir" >dmg.out 2>dmg.err &&
	test_grep dmg dmg.err
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'umount tears the view down' '
	"$tiler_bin" umount "$work_dir" >umount.out 2>umount.err &&
	test_grep -E "^unmounted " umount.out
'

# Simulate git core.symlinks=false: replace the leaf symlink with a
# regular file whose content is the absolute NAS path. The symlink
# must be removed first, because ">" through it would truncate the
# NAS source instead of the leaf.
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'path-ref leaf mounts like a symlink' '
	test_path_is_symlink "$payload" &&
	rm "$payload" &&
	printf "%s\n" "$img" >"$payload" &&
	test_path_is_file "$payload" &&
	! test -L "$payload" &&
	mkdir -p "$work_dir" &&
	"$tiler_bin" mount --file "$img" "$lib_name" "$work_dir" >pathref.out 2>pathref.err &&
	test_grep -E "^mounted " pathref.out &&
	test_grep "hello vendor" "$work_dir/README.txt" &&
	test_path_is_file "$work_dir/extra.txt" &&
	"$tiler_bin" umount "$work_dir" >pathref-umount.out 2>pathref-umount.err &&
	test_grep -E "^unmounted " pathref-umount.out
'

test_done
