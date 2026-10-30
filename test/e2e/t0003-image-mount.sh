#!/bin/sh
# Verify mounted source contents and persistent upper writes, then refuse
# a conflicting image without changing the live view. Real mounts need
# the Linux FUSE toolchain.

test_description='tiler builds, mounts, remounts, and refuses a conflicting image'

. "$(dirname "$0")/lib-tiler.sh"

# tiler_bin, TILER, LINUX, and FUSE_TOOLCHAIN come from sharness.d/tiler.sh.

# HOME is the per-run trash directory, so tiler's default layout
# ($HOME/.tiler/{images,sessions,cache}) is already sandboxed and
# needs no --*-dir flags. The mount target must sit OUTSIDE the runtime
# tree and the reserved directories, so keep it under $HOME/work.
src_dir=$HOME/src/app
work_dir=$HOME/work/app
src2_dir=$HOME/src/tools
work2_dir=$HOME/work/tools

# Guarantee every FUSE mount is torn down before the framework removes
# the trash directory, even if an assertion fails or the run is
# interrupted. `tiler umount` is idempotent; fusermount3 -uz is the
# fallback for a view whose daemon already died.
if test -n "$tiler_bin"
then
	test_atexit '
		for _m in "$work_dir" "$work2_dir"
		do
			"$tiler_bin" umount "$_m" >/dev/null 2>&1 ||
			"$fusermount3_bin" -uz "$_m" >/dev/null 2>&1 ||
			true
		done
	'
fi

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'build a full image from a source tree' '
	mkdir -p "$src_dir/docs" &&
	echo "hello tiler" >"$src_dir/README.txt" &&
	echo "# Guide" >"$src_dir/docs/guide.md" &&
	"$tiler_bin" build --image alice/app "$src_dir" >build.out 2>build.err &&
	test_grep -E "^full alice/app [0-9a-f]+" build.out &&
	image=$HOME/.tiler/images/alice/app/_objects/$(cut -d" " -f3 build.out)/image.sqsh &&
	test_path_is_file "$image" &&
	test_path_is_missing "${image%.sqsh}.dmg"
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'mount the image read-write with an upper layer' '
	mkdir -p "$work_dir" &&
	"$tiler_bin" mount alice/app "$work_dir" >mount.out 2>mount.err &&
	test_grep -E "^mounted " mount.out &&
	test_cmp "$src_dir/README.txt" "$work_dir/README.txt" &&
	test_cmp "$src_dir/docs/guide.md" "$work_dir/docs/guide.md" &&
	echo edited >"$work_dir/NEW.txt" &&
	test_path_is_file "$work_dir/NEW.txt"
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'repeat mount of the same view is idempotent' '
	"$tiler_bin" mount alice/app "$work_dir" >remount.out 2>remount.err &&
	test_grep -E "^already mounted " remount.out &&
	test_cmp "$src_dir/README.txt" "$work_dir/README.txt" &&
	echo edited >expect-upper &&
	test_cmp expect-upper "$work_dir/NEW.txt"
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'a second image builds with a distinct id' '
	mkdir -p "$src2_dir" &&
	echo "#!/bin/sh" >"$src2_dir/run.sh" &&
	"$tiler_bin" build --image bob/tools "$src2_dir" >build2.out 2>build2.err &&
	test_grep -E "^full bob/tools [0-9a-f]+" build2.out &&
	id1=$(cut -d" " -f3 build.out) &&
	id2=$(cut -d" " -f3 build2.out) &&
	test -n "$id1" && test -n "$id2" &&
	test "$id1" != "$id2"
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'the second image mounts at its own target' '
	mkdir -p "$work2_dir" &&
	"$tiler_bin" mount bob/tools "$work2_dir" >mount2.out 2>mount2.err &&
	test_grep -E "^mounted " mount2.out &&
	test_path_is_file "$work2_dir/run.sh" &&
	test_path_is_missing "$work2_dir/README.txt"
'

# work_dir is still live serving alice/app. Mounting a different image
# there must be refused as a conflict, not silently restacked: the exit
# is non-zero, the error names the refusal, and the live view still
# serves alice/app (README.txt present, run.sh absent).
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'a different image on a live view is refused' '
	test_expect_code 1 "$tiler_bin" mount bob/tools "$work_dir" >conflict.out 2>conflict.err &&
	test_must_be_empty conflict.out &&
	test_grep "already mounts a different view" conflict.err &&
	test_grep "refusing to restack it silently" conflict.err &&
	test_cmp "$src_dir/README.txt" "$work_dir/README.txt" &&
	test_cmp expect-upper "$work_dir/NEW.txt" &&
	test_path_is_missing "$work_dir/run.sh"
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'umount tears both views down' '
	"$tiler_bin" umount "$work_dir" &&
	"$tiler_bin" umount "$work2_dir" &&
	test_path_is_missing "$work_dir/README.txt" &&
	test_path_is_missing "$work_dir/NEW.txt" &&
	test_path_is_missing "$work2_dir/run.sh"
'

test_done
