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

# Both close paths consume the full above and leave no session data behind.
for tiler_rm_mode in auto named
do
	test_expect_success TILER,LINUX,SQUASHFS_PACK,WORKFLOW_GIT,FUSE_TOOLCHAIN "$tiler_rm_mode remove-on-close session purges its view and activity" '
		if test "$tiler_rm_mode" = auto
		then
			rm_point=$standard/mnt-auto-rm
		else
			rm_point=$standard/mnt-named-rm
		fi &&
		mkdir -p "$rm_point" &&
		(
			cd "$standard" &&
			if test "$tiler_rm_mode" = auto
			then
				"$tiler_bin" mount --rm standard/app "$rm_point" 2>"$HOME/rm-mount.err"
			else
				"$tiler_bin" session new --rm scratch-edit >"$HOME/rm-new.out" 2>"$HOME/rm-new.err" &&
				"$tiler_bin" session show scratch-edit >"$HOME/rm-before.out" 2>"$HOME/rm-before.err" &&
				"$tiler_bin" mount --session scratch-edit standard/app "$rm_point" 2>"$HOME/rm-mount.err"
			fi
		) >rm-mount.out &&
		test_grep -F "mounted $rm_point" rm-mount.out &&
		if test "$tiler_rm_mode" = auto
		then
			auto_rm_session=$(sed -n "s/^new session created: //p" rm-mount.out) &&
			test -n "$auto_rm_session" &&
			rm_session=$auto_rm_session
		else
			rm_session=scratch-edit &&
			test_grep "^created scratch-edit$" rm-new.out &&
			test_grep "^scratch-edit · active" rm-before.out &&
			test_grep "remove on close" rm-before.out
		fi &&
		test_path_is_file "$sessions/$rm_session/session.json" &&
		test_path_is_dir "$sessions/$rm_session/runtime/m1/upper" &&
		test_path_is_file "$logs/sessions/$rm_session.log" &&
		test_path_is_file "$logs/sessions/$rm_session.log.lock" &&
		(
			cd "$standard" &&
			"$tiler_bin" status "$rm_point" 2>"$HOME/rm-status.err"
		) >rm-status.out &&
		test_grep "^$rm_session · active" rm-status.out &&
		test_grep "standard/app:.*\[rw · mounted · healthy\]" rm-status.out &&
		test_grep -F "→  ${rm_point##*/}  " rm-status.out &&
		(
			cd "$standard" &&
			"$tiler_bin" session show "$rm_session" --json 2>"$HOME/rm-show.err"
		) >rm-show.json &&
		test_grep -F "\"name\": \"$rm_session\"" rm-show.json &&
		test_grep -F "\"remove_on_close\": true" rm-show.json &&
		test_grep -F "\"full_id\": \"$base_id\"" rm-show.json &&
		test_grep -F "\"mountpoint\": \"$rm_point\"" rm-show.json &&
		test_cmp "$prepare/README.txt" "$rm_point/README.txt" &&
		printf "scratch edit\n" >rm-expect &&
		cp rm-expect "$rm_point/scratch.txt" &&
		test_cmp rm-expect "$sessions/$rm_session/runtime/m1/upper/scratch.txt" &&
		(
			cd "$standard" &&
			if test "$tiler_rm_mode" = auto
			then
				"$tiler_bin" umount "$rm_point" 2>"$HOME/rm-close.err"
			else
				"$tiler_bin" session close "$rm_session" 2>"$HOME/rm-close.err"
			fi
		) >rm-close.out &&
		test_path_is_dir "$rm_point" &&
		find "$rm_point" -mindepth 1 -print >rm-remaining &&
		test_must_be_empty rm-remaining &&
		test_path_is_missing "$sessions/$rm_session" &&
		test_path_is_missing "$logs/sessions/$rm_session.log" &&
		test_path_is_missing "$logs/sessions/$rm_session.log.lock" &&
		(
			cd "$standard" &&
			test_expect_code 1 "$tiler_bin" session show "$rm_session" 2>"$HOME/rm-missing.err"
		) >rm-missing.out &&
		test_must_be_empty rm-missing.out &&
		test_grep "not found" rm-missing.err &&
		test_path_is_missing "$sessions/$rm_session/session.json"
	'
done

test_done
