#!/bin/sh
#
# tiler end-to-end test: activity-aware GC without a mount.
#
# Session GC runs with only the tiler binary. Image GC publishes one
# full version with mksquashfs and skips that half when the packer is
# missing. Neither half mounts, so a host without /dev/fuse still runs
# the cases it can.
#
# The case checks the preview contract, one-time initialization, legacy
# session_max_age parsing, and removal of a session's upper data. Parent
# chain propagation and ancestor retention stay in t0004, which has a
# real incremental stack.

test_description='tiler GC previews and removes expired objects without a mount'

tiler_case_dir=$(cd "$(dirname "$0")" && pwd)
TEST_LIB_DIRECTORY=$(cd "$tiler_case_dir/../git-test-lib" && pwd)
export TEST_LIB_DIRECTORY
TEST_DIRECTORY=$tiler_case_dir
export TEST_DIRECTORY

. "$TEST_LIB_DIRECTORY/test-lib.sh"

# tiler_bin, TILER, LINUX, and SQUASHFS_PACK come from sharness.d/tiler.sh.

# rewrite_activity sets every record time. GC reads the last line, so
# one old timestamp is enough to make the object eligible.
rewrite_activity() {
	sed "s/\"time\":\"[^\"]*\"/\"time\":\"$2\"/" "$1" >"$1.rewritten" &&
	mv "$1.rewritten" "$1"
}

sessions=$HOME/.tiler/sessions
images=$HOME/.tiler/images
src_dir=$HOME/src/app

test_expect_success TILER 'legacy session age settings stay parseable and do not drive GC' '
	printf "%s\n" \
		"version: 1" \
		"session_max_age: 0s" \
		"session_autoclose_stale: true" >"$HOME/.tiler.yaml" &&
	"$tiler_bin" version >ver.out 2>ver.err &&
	test_grep "^tiler " ver.out &&
	test_grep "session_max_age: 0s" "$HOME/.tiler.yaml" &&
	test_grep "session_autoclose_stale: true" "$HOME/.tiler.yaml" &&
	test_expect_code 2 "$tiler_bin" session gc --expire -1 >neg.out 2>neg.err &&
	test_grep "nonnegative" neg.err &&
	test_expect_code 2 "$tiler_bin" images gc --expire 1.5 >frac.out 2>frac.err &&
	test_grep "nonnegative" frac.err
'

test_expect_success TILER 'session GC previews, initializes once, and then deletes upper data' '
	"$tiler_bin" session new idle >new.out 2>new.err &&
	test_grep "^created idle$" new.out &&
	mkdir -p "$sessions/idle/runtime/m1/upper" &&
	echo keep >"$sessions/idle/runtime/m1/upper/work" &&
	test_grep "session.create" "$HOME/.tiler/logs/sessions/idle.log" &&
	"$tiler_bin" session gc >fresh.out 2>fresh.err &&
	test_grep "No session expired" fresh.out &&
	test_grep ! "Type yes" fresh.err &&
	test_path_is_file "$sessions/idle/runtime/m1/upper/work" &&
	rewrite_activity "$HOME/.tiler/logs/sessions/idle.log" "2020-01-01T00:00:00.000000000Z" &&
	"$tiler_bin" session gc >aged.out 2>aged.err &&
	test_grep "Expired sessions" aged.out &&
	test_grep "  idle  " aged.out &&
	test_grep "runtime/" aged.out &&
	test_grep "Nothing removed" aged.err &&
	printf "yes\n" | "$tiler_bin" session gc >pipe.out 2>pipe.err &&
	test_grep "Nothing removed" pipe.err &&
	test_path_is_file "$sessions/idle/runtime/m1/upper/work" &&
	rm -f "$HOME/.tiler/logs/sessions/idle.log" &&
	"$tiler_bin" session gc --expire 0 >init1.out 2>init1.err &&
	test_grep "No session expired" init1.out &&
	test_grep "kept idle: initialized" init1.err &&
	test_grep "activity.initialize" "$HOME/.tiler/logs/sessions/idle.log" &&
	cp "$HOME/.tiler/logs/sessions/idle.log" idle.before &&
	"$tiler_bin" session gc --expire 0 >init2.out 2>init2.err &&
	test_grep ! initialized init2.err &&
	test_cmp idle.before "$HOME/.tiler/logs/sessions/idle.log" &&
	test_grep "Expired sessions" init2.out &&
	test_grep "Nothing removed" init2.err &&
	test_path_is_file "$sessions/idle/runtime/m1/upper/work" &&
	"$tiler_bin" sessions gc --expire 0 --yes >yes.out 2>yes.err &&
	test_grep "^removed idle$" yes.out &&
	test_grep "removed 1 of 1 approved sessions, kept 0" yes.out &&
	test_path_is_missing "$sessions/idle"
'

test_expect_success TILER 'image GC with an empty library only previews' '
	"$tiler_bin" images gc >empty.out 2>empty.err &&
	test_grep "No image objects eligible" empty.out &&
	test_grep ! "Type yes" empty.err
'

test_expect_success TILER,LINUX,SQUASHFS_PACK 'image GC previews a full version, initializes once, and then deletes it' '
	mkdir -p "$src_dir" &&
	echo "hello tiler" >"$src_dir/README.txt" &&
	"$tiler_bin" build --image alice/app "$src_dir" >build.out 2>build.err &&
	test_grep -E "^full alice/app [0-9a-f]+" build.out &&
	id=$(cut -d" " -f3 build.out) &&
	test -n "$id" &&
	log=$HOME/.tiler/logs/images/alice/app/$id.log &&
	test_grep "\"event\":\"image.create.full\"" "$log" &&
	test_grep ! "image.dependency.activity" "$log" &&
	"$tiler_bin" images gc --expire 0 >prev.out 2>prev.err &&
	test_grep "Image GC candidates" prev.out &&
	test_grep "expired  alice/app:$id" prev.out &&
	test_grep "external source files are preserved" prev.out &&
	test_grep "Nothing removed" prev.err &&
	test_grep ! "^removed " prev.out &&
	test_path_is_file "$images/alice/app/_objects/$id/image.sqsh" &&
	rm -f "$log" &&
	"$tiler_bin" images gc --expire 0 >i1.out 2>i1.err &&
	test_grep "No image objects eligible" i1.out &&
	test_grep "kept alice/app:$id: initialized" i1.err &&
	test_grep "activity.initialize" "$log" &&
	cp "$log" image.before &&
	"$tiler_bin" images gc --expire 0 >i2.out 2>i2.err &&
	test_grep ! initialized i2.err &&
	test_cmp image.before "$log" &&
	test_grep "expired  alice/app:$id" i2.out &&
	test_grep "Nothing removed" i2.err &&
	test_path_is_file "$images/alice/app/_objects/$id/image.sqsh" &&
	"$tiler_bin" image gc --expire 0 --yes >del.out 2>del.err &&
	test_grep "^removed alice/app:$id$" del.out &&
	test_grep "removed 1 of 1 approved image objects, skipped 0" del.out &&
	test_path_is_missing "$images/alice/app"
'

test_done
