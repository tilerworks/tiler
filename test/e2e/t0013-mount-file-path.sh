#!/bin/sh
#
# tiler end-to-end test: mount a squashfs by absolute path outside
# images_dir, with no meta.yaml beside it.
#
# Companion to t0003-image-mount.sh (library owner/repo mounts). This
# case covers the third-party / NAS file-path form:
#
#   1. pack a bare squashfs with mksquashfs into a directory that is
#      not under $HOME/.tiler/images and carries no meta.yaml,
#   2. mount that absolute path into a named session,
#   3. confirm the view serves the packed content,
#   4. confirm the session record stores the path as given and leaves
#      owner/repo/full_id empty,
#   5. umount.
#
# A real mount needs a writable /dev/fuse plus the squashfs and overlay
# toolchain that `tiler doctor` treats as fail-level. FUSE_TOOLCHAIN from
# sharness.d/tiler.sh models exactly that, so the case skips cleanly -- rather
# than false-fails -- on a host without the toolchain.
#
# Scope guard: this case asserts file-path mount + session identity
# only. It does not cover library mounts, --verify (rejected for file
# paths), split blocks, or incremental builds.

test_description='tiler mounts a bare squashfs path outside images_dir'

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
# images_dir; the mount target stays under $HOME/work.
src_dir=$HOME/src/vendor
nas_dir=$HOME/nas
img=$nas_dir/vendor.sqsh
work_dir=$HOME/work/vendor
session_json=$HOME/.tiler/sessions/ext/session.json
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

# Pack with mksquashfs directly so the payload never enters the library
# and never grows a meta.yaml. The path must stay outside images_dir.
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'pack a bare squashfs outside images_dir without meta' '
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
	test_path_is_missing "$nas_dir/meta.yaml" &&
	test_path_is_missing "$images_dir/meta.yaml" &&
	! find "$nas_dir" -name meta.yaml | grep -q .
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'mount the absolute path and serve the content' '
	"$tiler_bin" session new ext >new.out 2>new.err &&
	test_grep -E "^created ext$" new.out &&
	mkdir -p "$work_dir" &&
	"$tiler_bin" mount --session ext "$img" "$work_dir" >mount.out 2>mount.err &&
	test_grep -E "^mounted " mount.out &&
	test_grep "hello vendor" "$work_dir/README.txt" &&
	test_path_is_file "$work_dir/docs/guide.md"
'

# The session action stores the reference as given (here the absolute
# path) and leaves library identity empty. session show --json and the
# on-disk record both omit owner/repo/full_id for this form.
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'session records the image path with empty library identity' '
	"$tiler_bin" session show ext >show.out 2>show.err &&
	test_grep "📁.*vendor.sqsh" show.out &&
	test_grep -E "\[rw · mounted · healthy\]" show.out &&
	"$tiler_bin" session show ext --json >showj.out 2>showj.err &&
	test_grep -F "\"image\": \"$img\"" showj.out &&
	test_grep ! "\"owner\"" showj.out &&
	test_grep ! "\"repo\"" showj.out &&
	test_grep ! "\"full_id\"" showj.out &&
	test_path_is_file "$session_json" &&
	test_grep -F "\"image\": \"$img\"" "$session_json" &&
	test_grep ! "\"owner\"" "$session_json" &&
	test_grep ! "\"repo\"" "$session_json" &&
	test_grep ! "\"full_id\"" "$session_json"
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'umount tears the view down' '
	"$tiler_bin" umount "$work_dir" >umount.out 2>umount.err &&
	test_grep -E "^unmounted " umount.out
'

test_done
