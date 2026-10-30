#!/bin/sh

test_description='Linux standard workflow publishes and edits a Git-backed image'

. "$(dirname "$0")/lib-tiler.sh"
. "$TEST_DIRECTORY/lib-workflow.sh"

standard=$HOME/ws-standard
prepare=$standard/prepare
images=$standard/.tiler/images/standard/app
sessions=$standard/.tiler/sessions
logs=$standard/.tiler/logs

# Later lifecycle steps share this project and its immutable version history.
test_expect_success TILER,LINUX,SQUASHFS_PACK,WORKFLOW_GIT 'prepare an isolated project with a tagged Git source' '
	workflow_config "$standard" &&
	mkdir -p "$prepare/docs" &&
	workflow_git_init "$prepare" &&
	printf "base content\n" >"$prepare/README.txt" &&
	printf "base guide\n" >"$prepare/docs/guide.txt" &&
	printf "remove in the increment\n" >"$prepare/obsolete.txt" &&
	git -C "$prepare" add . &&
	git -C "$prepare" commit -m "Create initial source" &&
	git -C "$prepare" tag -a -m "Initial version" v1 &&
	git -C "$prepare" rev-parse HEAD >base-commit &&
	git -C "$prepare" rev-parse "v1^{commit}" >base-tag &&
	test_cmp base-commit base-tag &&
	git -C "$prepare" cat-file -t v1 >tag-type &&
	printf "tag\n" >expect-tag-type &&
	test_cmp expect-tag-type tag-type &&
	test_atexit '\''
		(
			cd "$standard" || exit
			for tiler_point in "$standard"/mnt-*
			do
				"$tiler_bin" umount "$tiler_point" >/dev/null 2>&1 ||
				"$fusermount3_bin" -uz "$tiler_point" >/dev/null 2>&1 || true
			done
		)
	'\''
'

test_expect_success TILER,LINUX,SQUASHFS_PACK,WORKFLOW_GIT 'publish the initial full with metadata and an activity log' '
	(
		cd "$prepare" &&
		"$tiler_bin" build -I standard/app -f --format squashfs -m initial 2>"$HOME/base.err"
	) >base.out &&
	test_grep -E "^full standard/app [0-9a-f]{64}$" base.out &&
	base_id=$(cut -d" " -f3 base.out) &&
	object=$images/_objects/$base_id &&
	test_path_is_file "$object/image.sqsh" &&
	test_path_is_file "$object/image.yaml" &&
	test_path_is_file "$images/meta.yaml" &&
	test_path_is_missing "$object/blk.000" &&
	sha256sum "$object/image.sqsh" >base.sum &&
	base_sum=$(cut -d" " -f1 base.sum) &&
	test "$base_sum" = "$base_id" &&
	for tiler_meta in "$images/meta.yaml" "$object/image.yaml"
	do
		test_grep "^name: standard/app$" "$tiler_meta" &&
		test_grep -E "^[[:space:]]*(- )?id: $base_id$" "$tiler_meta" &&
		test_grep -E "^[[:space:]]*sha256: $base_id$" "$tiler_meta" &&
		test_grep -E "^[[:space:]]*kind: full$" "$tiler_meta" &&
		test_grep -E "^[[:space:]]*description: initial$" "$tiler_meta" || return 1
	done &&
	(
		cd "$standard" &&
		"$tiler_bin" images show "standard/app:$base_id" 2>"$HOME/base-show.err"
	) >base-show.out &&
	test_grep -E "^  id +$base_id$" base-show.out &&
	test_grep -E "^  parent +-$" base-show.out &&
	test_grep -E "^  format +squashfs/" base-show.out &&
	test_grep -E "^  payload +single$" base-show.out &&
	test_grep -E "^  description +initial$" base-show.out &&
	test_path_is_file "$logs/images/standard/app/$base_id.log" &&
	test_grep "image.create.full" "$logs/images/standard/app/$base_id.log"
'

test_done
