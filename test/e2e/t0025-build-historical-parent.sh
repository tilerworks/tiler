#!/bin/sh

test_description='incremental builds branch from the mounted immutable version'

tiler_case_dir=$(cd "$(dirname "$0")" && pwd)
TEST_LIB_DIRECTORY=$(cd "$tiler_case_dir/../git-test-lib" && pwd)
export TEST_LIB_DIRECTORY
TEST_DIRECTORY=$tiler_case_dir
export TEST_DIRECTORY
. "$TEST_LIB_DIRECTORY/test-lib.sh"

src=$HOME/src/app
view=$HOME/work/view
branch=$HOME/work/branch
verify=$HOME/work/verify

test_atexit '
 for point in "$view" "$branch" "$verify"
 do
  "$tiler_bin" umount "$point" >/dev/null 2>&1 || true
 done
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'publish a full image and advance its tip' '
 mkdir -p "$src" "$view" "$branch" "$verify" &&
 echo base >"$src/base" &&
 "$tiler_bin" build -s 0 -I alice/app "$src" >base.out &&
 base=$(cut -d" " -f3 base.out) &&
 "$tiler_bin" mount alice/app "$view" &&
 echo old-tip >"$view/old-tip" &&
 (cd "$view" && "$tiler_bin" build -s 0 -i) >tip.out &&
 tip=$(cut -d" " -f3 tip.out)
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'build from historical full uses that full as parent' '
 "$tiler_bin" mount "alice/app:${base%${base#??????????}}" "$branch" &&
 echo file1 >"$branch/file1" &&
 printf binary >"$branch/file2.bin" &&
 (cd "$branch" && "$tiler_bin" build -s 0 -m "add file1 and file2.bin" -i) >branch.out &&
 child=$(cut -d" " -f3 branch.out) &&
 "$tiler_bin" images show "alice/app:$child" >child.out &&
 test_grep -E "^  parent +$base$" child.out &&
 "$tiler_bin" images ls >tree.out &&
 test_grep "^    ${tip%${tip#??????????}}  " tree.out &&
 test_grep "^    ${child%${child#??????????}}  " tree.out &&
 "$tiler_bin" mount "alice/app:$child" "$verify" &&
 test_path_is_file "$verify/base" &&
 test_path_is_file "$verify/file1" &&
 test_path_is_file "$verify/file2.bin" &&
 test_path_is_missing "$verify/old-tip"
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'build from historical incremental uses its pinned tip' '
 "$tiler_bin" umount "$verify" &&
 "$tiler_bin" mount "alice/app:$tip" "$verify" &&
 echo next >"$verify/next" &&
 (cd "$verify" && "$tiler_bin" build -s 0 -i) >next.out &&
 next=$(cut -d" " -f3 next.out) &&
 "$tiler_bin" images show "alice/app:$next" >next-show.out &&
 test_grep -E "^  parent +$tip$" next-show.out &&
 "$tiler_bin" images ls >next-tree.out &&
 test_grep "^      ${next%${next#??????????}}  " next-tree.out &&
 "$tiler_bin" umount "$verify" &&
 "$tiler_bin" mount "alice/app:$next" "$verify" &&
 test_path_is_file "$verify/old-tip" &&
 test_path_is_file "$verify/next" &&
 test_path_is_missing "$verify/file1"
'

test_done
