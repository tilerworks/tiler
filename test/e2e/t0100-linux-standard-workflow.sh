#!/bin/sh

test_description='Linux standard workflow publishes and edits a Git-backed image'

# Git allocator instrumentation can abort the external squashfuse daemon.
TEST_NO_MALLOC_CHECK=1
. "$(dirname "$0")/lib-tiler.sh"
. "$TEST_DIRECTORY/lib-workflow.sh"

standard=$HOME/ws-standard
prepare=$standard/prepare
images=$standard/.tiler/images/standard/app
sessions=$standard/.tiler/sessions
logs=$standard/.tiler/logs

# Later lifecycle steps share this project and its immutable version history.
test_expect_success TILER,LINUX,SQUASHFS_PACK,WORKFLOW_GIT,WORKFLOW_SQUASHFS_CLIENT 'prepare an isolated project with a tagged Git source' '
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

test_expect_success TILER,LINUX,SQUASHFS_PACK,WORKFLOW_GIT,WORKFLOW_SQUASHFS_CLIENT 'publish the initial full with metadata and an activity log' '
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

test_expect_success TILER,LINUX,SQUASHFS_PACK,WORKFLOW_GIT,FUSE_TOOLCHAIN 'an open mounted session keeps its upper and stays out of GC candidates' '
	gc_point=$standard/mnt-gc &&
	gc_record=$sessions/gc-check/session.json &&
	gc_upper=$sessions/gc-check/runtime/m1/upper &&
	gc_log=$logs/sessions/gc-check.log &&
	mkdir -p "$gc_point" &&
	(
		cd "$standard" &&
		"$tiler_bin" session new gc-check >"$HOME/gc-new.out" 2>"$HOME/gc-new.err" &&
		"$tiler_bin" mount --session gc-check standard/app "$gc_point" 2>"$HOME/gc-mount.err"
	) >gc-mount.out &&
	printf "retained session work\n" >gc-expect &&
	cp gc-expect "$gc_point/work.txt" &&
	(
		cd "$standard" &&
		"$tiler_bin" session open gc-check 2>"$HOME/gc-open.err"
	) >gc-open.out &&
	test_grep "^opened gc-check$" gc-open.out &&
	test_cmp "$prepare/README.txt" "$gc_point/README.txt" &&
	test_cmp gc-expect "$gc_point/work.txt" &&
	test_cmp gc-expect "$gc_upper/work.txt" &&
	grep "\"type\": \"mount\"" "$gc_record" >gc-mount-actions &&
	test_line_count = 1 gc-mount-actions &&
	cp "$gc_record" gc-mounted-record.before &&
	cp "$gc_log" gc-mounted-log.before &&
	(
		cd "$standard" &&
		"$tiler_bin" session gc --expire 0 --dry-run 2>"$HOME/gc-mounted.err"
	) >gc-mounted.out &&
	test_grep "No session expired" gc-mounted.out &&
	test_grep ! "gc-check" gc-mounted.out &&
	test_grep "^kept gc-check: attached" gc-mounted.err &&
	test_cmp gc-mounted-record.before "$gc_record" &&
	test_cmp gc-mounted-log.before "$gc_log" &&
	test_cmp gc-expect "$gc_point/work.txt" &&
	test_cmp gc-expect "$gc_upper/work.txt"
'

test_expect_success TILER,LINUX,SQUASHFS_PACK,WORKFLOW_GIT,FUSE_TOOLCHAIN 'a closed session survives retention and zero-day GC previews' '
	(
		cd "$standard" &&
		"$tiler_bin" session close gc-check 2>"$HOME/gc-close.err"
	) >gc-close.out &&
	test_grep "^closed gc-check$" gc-close.out &&
	find "$gc_point" -mindepth 1 -print >gc-remaining &&
	test_must_be_empty gc-remaining &&
	test_path_is_file "$gc_record" &&
	test_path_is_file "$gc_log" &&
	test_path_is_file "$gc_log.lock" &&
	test_cmp gc-expect "$gc_upper/work.txt" &&
	cp "$gc_record" gc-closed-record.before &&
	cp "$gc_log" gc-closed-log.before &&
	(
		cd "$standard" &&
		"$tiler_bin" session gc --expire 10 2>"$HOME/gc-young.err"
	) >gc-young.out &&
	test_grep "No session expired" gc-young.out &&
	test_cmp gc-closed-record.before "$gc_record" &&
	test_cmp gc-closed-log.before "$gc_log" &&
	test_cmp gc-expect "$gc_upper/work.txt" &&
	(
		cd "$standard" &&
		"$tiler_bin" session gc --expire 0 --dry-run 2>"$HOME/gc-preview.err"
	) >gc-preview.out &&
	test_grep "^Expired sessions" gc-preview.out &&
	test_grep -E "^  gc-check +closed +last active " gc-preview.out &&
	test_grep ! "^removed " gc-preview.out &&
	test_grep ! "Type yes" gc-preview.err &&
	test_cmp gc-closed-record.before "$gc_record" &&
	test_cmp gc-closed-log.before "$gc_log" &&
	test_cmp gc-expect "$gc_upper/work.txt"
'

test_expect_success TILER,LINUX,SQUASHFS_PACK,WORKFLOW_GIT,FUSE_TOOLCHAIN 'approved zero-day GC removes the closed session and all of its data' '
	(
		cd "$standard" &&
		"$tiler_bin" session gc --expire 0 --yes 2>"$HOME/gc-delete.err"
	) >gc-delete.out &&
	test_grep "^removed gc-check$" gc-delete.out &&
	test_grep "^removed 1 of 1 approved sessions, kept 0$" gc-delete.out &&
	test_path_is_missing "$sessions/gc-check" &&
	test_path_is_missing "$gc_log" &&
	test_path_is_missing "$gc_log.lock" &&
	find "$gc_point" -mindepth 1 -print >gc-deleted-remaining &&
	test_must_be_empty gc-deleted-remaining
'

test_expect_success TILER,LINUX,SQUASHFS_PACK,WORKFLOW_GIT,FUSE_TOOLCHAIN 'an increment advances the desired pin while preserving the live upper' '
	edit_point=$standard/mnt-edit &&
	mkdir -p "$edit_point" &&
	(
		cd "$standard" &&
		"$tiler_bin" mount "standard/app:$base_id" "$edit_point" 2>"$HOME/edit-mount.err"
	) >edit-mount.out &&
	edit_session=$(sed -n "s/^new session created: //p" edit-mount.out) &&
	test -n "$edit_session" &&
	edit_record=$sessions/$edit_session/session.json &&
	edit_upper=$sessions/$edit_session/runtime/m1/upper &&
	printf "published README revision\n" >delta-readme &&
	printf "published addition\n" >delta-addition &&
	cp delta-readme "$edit_point/README.txt" &&
	cp delta-addition "$edit_point/published.txt" &&
	rm "$edit_point/obsolete.txt" &&
	git -C "$edit_point" add . &&
	git -C "$edit_point" commit -m "Publish the first increment" &&
	git -C "$edit_point" tag -a -m "Incremental version" v2 &&
	git -C "$edit_point" rev-parse HEAD >delta-commit &&
	git -C "$edit_point" rev-parse "v2^{commit}" >delta-tag &&
	test_cmp delta-commit delta-tag &&
	git -C "$edit_point" cat-file -t v2 >delta-tag-type &&
	test_cmp expect-tag-type delta-tag-type &&
	(
		cd "$edit_point" &&
		"$tiler_bin" build -i --format squashfs 2>"$HOME/delta-build.err"
	) >delta-build.out &&
	test_grep -E "^incr standard/app [0-9a-f]{64}$" delta-build.out &&
	delta_id=$(cut -d" " -f3 delta-build.out) &&
	test "$delta_id" != "$base_id" &&
	test_grep -F "\"full_id\": \"$base_id\"" "$edit_record" &&
	test_grep -F "\"tip_id\": \"$delta_id\"" "$edit_record" &&
	test_grep -F "\"served_id\": \"$base_id\"" "$edit_record" &&
	test_grep -F "\"served_tip\": \"$base_id\"" "$edit_record" &&
	test_grep -E "^[[:space:]]*parent: $base_id$" "$images/_objects/$delta_id/image.yaml" &&
	printf "a longer README revision after publication\n" >post-build-readme &&
	printf "work not included in the increment\n" >post-build-addition &&
	cp post-build-readme "$edit_point/README.txt" &&
	cp post-build-addition "$edit_point/pending.txt" &&
	(
		cd "$standard" &&
		"$tiler_bin" umount "$edit_point" 2>"$HOME/edit-umount.err"
	) >edit-umount.out &&
	find "$edit_point" -mindepth 1 -print >edit-unmounted &&
	test_must_be_empty edit-unmounted &&
	test_cmp delta-addition "$edit_upper/published.txt" &&
	test_cmp post-build-readme "$edit_upper/README.txt" &&
	test_cmp post-build-addition "$edit_upper/pending.txt" &&
	test_grep -F "\"tip_id\": \"$delta_id\"" "$edit_record" &&
	test_grep -F "\"served_id\": \"$base_id\"" "$edit_record" &&
	test_grep -F "\"served_tip\": \"$base_id\"" "$edit_record"
'

test_expect_success TILER,LINUX,SQUASHFS_PACK,WORKFLOW_GIT,FUSE_TOOLCHAIN 'replaying the edited session prunes published files and keeps subsequent work' '
	(
		cd "$standard" &&
		"$tiler_bin" session open "$edit_session" 2>"$HOME/edit-open.err"
	) >edit-open.out &&
	test_grep "^opened $edit_session$" edit-open.out &&
	test_grep -F "\"full_id\": \"$base_id\"" "$edit_record" &&
	test_grep -F "\"tip_id\": \"$delta_id\"" "$edit_record" &&
	test_grep ! -F "\"served_id\":" "$edit_record" &&
	test_grep ! -F "\"served_tip\":" "$edit_record" &&
	test_path_is_missing "$edit_upper/published.txt" &&
	test_cmp delta-addition "$edit_point/published.txt" &&
	test_cmp post-build-readme "$edit_point/README.txt" &&
	test_cmp post-build-readme "$edit_upper/README.txt" &&
	test_cmp post-build-addition "$edit_point/pending.txt" &&
	test_cmp post-build-addition "$edit_upper/pending.txt" &&
	test_cmp "$prepare/docs/guide.txt" "$edit_point/docs/guide.txt" &&
	test_path_is_missing "$edit_point/obsolete.txt" &&
	git -C "$edit_point" rev-parse HEAD >replayed-commit &&
	git -C "$edit_point" rev-parse "v2^{commit}" >replayed-tag &&
	test_cmp delta-commit replayed-commit &&
	test_cmp delta-tag replayed-tag
'

test_expect_success TILER,LINUX,SQUASHFS_PACK,WORKFLOW_GIT,FUSE_TOOLCHAIN 'a fresh incremental view serves the publication without subsequent work' '
	verify_point=$standard/mnt-verify &&
	mkdir -p "$verify_point" &&
	(
		cd "$standard" &&
		"$tiler_bin" mount --rm "standard/app:$delta_id" "$verify_point" 2>"$HOME/delta-verify.err"
	) >delta-verify.out &&
	verify_session=$(sed -n "s/^new session created: //p" delta-verify.out) &&
	test -n "$verify_session" &&
	test "$verify_session" != "$edit_session" &&
	test_cmp delta-readme "$verify_point/README.txt" &&
	test_cmp delta-addition "$verify_point/published.txt" &&
	test_cmp "$prepare/docs/guide.txt" "$verify_point/docs/guide.txt" &&
	test_path_is_missing "$verify_point/obsolete.txt" &&
	test_path_is_missing "$verify_point/pending.txt" &&
	git -C "$verify_point" rev-parse HEAD >published-commit &&
	git -C "$verify_point" rev-parse "v2^{commit}" >published-tag &&
	test_cmp delta-commit published-commit &&
	test_cmp delta-tag published-tag &&
	(
		cd "$standard" &&
		"$tiler_bin" umount "$verify_point" 2>"$HOME/delta-verify-umount.err"
	) >delta-verify-umount.out &&
	find "$verify_point" -mindepth 1 -print >delta-verify-unmounted &&
	test_must_be_empty delta-verify-unmounted &&
	test_path_is_missing "$sessions/$verify_session" &&
	test_path_is_missing "$logs/sessions/$verify_session.log" &&
	test_path_is_missing "$logs/sessions/$verify_session.log.lock"
'

# Each branch owns a distinct named session, with work left after publication.
for tiler_branch_kind in chain branch
do
	test_expect_success TILER,LINUX,SQUASHFS_PACK,WORKFLOW_GIT,FUSE_TOOLCHAIN "$tiler_branch_kind increment uses its explicitly mounted parent" '
		if test "$tiler_branch_kind" = chain
		then
			branch_parent=$delta_id
		else
			branch_parent=$base_id
		fi &&
		branch_point=$standard/mnt-$tiler_branch_kind &&
		branch_session=$tiler_branch_kind-edit &&
		mkdir -p "$branch_point" &&
		(
			cd "$standard" &&
			"$tiler_bin" session new "$branch_session" >"$HOME/$tiler_branch_kind-new.out" 2>"$HOME/$tiler_branch_kind-new.err" &&
			"$tiler_bin" mount --session "$branch_session" "standard/app:$branch_parent" "$branch_point" 2>"$HOME/$tiler_branch_kind-mount.err"
		) >"$tiler_branch_kind-mount.out" &&
		printf "%s published content\n" "$tiler_branch_kind" >"$tiler_branch_kind-published" &&
		cp "$tiler_branch_kind-published" "$branch_point/$tiler_branch_kind.txt" &&
		(
			cd "$branch_point" &&
			"$tiler_bin" build -i --format squashfs 2>"$HOME/$tiler_branch_kind-build.err"
		) >"$tiler_branch_kind-build.out" &&
		test_grep -E "^incr standard/app [0-9a-f]{64}$" "$tiler_branch_kind-build.out" &&
		branch_id=$(cut -d" " -f3 "$tiler_branch_kind-build.out") &&
		if test "$tiler_branch_kind" = chain
		then
			chain_tip_id=$branch_id
		else
			branch_tip_id=$branch_id
		fi &&
		test_grep -E "^[[:space:]]*parent: $branch_parent$" "$images/_objects/$branch_id/image.yaml" &&
		printf "%s unpublished work\n" "$tiler_branch_kind" >"$tiler_branch_kind-pending" &&
		cp "$tiler_branch_kind-pending" "$branch_point/$tiler_branch_kind-pending.txt" &&
		test_cmp "$tiler_branch_kind-pending" "$sessions/$branch_session/runtime/m1/upper/$tiler_branch_kind-pending.txt"
	'
done

test_expect_success TILER,LINUX,SQUASHFS_PACK,WORKFLOW_GIT,FUSE_TOOLCHAIN 'version details and the tree distinguish descendants from sibling branches' '
	(
		cd "$standard" &&
		"$tiler_bin" images show "standard/app:$chain_tip_id" >"$HOME/chain-show.out" 2>"$HOME/chain-show.err" &&
		"$tiler_bin" images show "standard/app:$branch_tip_id" >"$HOME/branch-show.out" 2>"$HOME/branch-show.err" &&
		"$tiler_bin" images show standard/app >"$HOME/latest-show.out" 2>"$HOME/latest-show.err" &&
		"$tiler_bin" images ls 2>"$HOME/branch-tree.err"
	) >branch-tree.out &&
	test_grep -E "^  parent +$delta_id$" chain-show.out &&
	test_grep -E "^  parent +$base_id$" branch-show.out &&
	test_grep -E "^  id +$branch_tip_id$" latest-show.out &&
	sed -n "s/^    \([0-9a-f]\{64\}\)$/\1/p" chain-show.out >chain-ancestors &&
	printf "%s\n%s\n" "$base_id" "$delta_id" >chain-ancestors.expect &&
	test_cmp chain-ancestors.expect chain-ancestors &&
	sed -n "s/^    \([0-9a-f]\{64\}\)$/\1/p" branch-show.out >branch-ancestors &&
	printf "%s\n" "$base_id" >branch-ancestors.expect &&
	test_cmp branch-ancestors.expect branch-ancestors &&
	test_grep "^     ├─ ${delta_id%${delta_id#??????????}}  " branch-tree.out &&
	test_grep "^     │  └─ ${chain_tip_id%${chain_tip_id#??????????}}  " branch-tree.out &&
	test_grep "^     └─ ${branch_tip_id%${branch_tip_id#??????????}} ★  " branch-tree.out &&
	grep "^[[:space:]]*- id:" "$images/meta.yaml" >branch-version-count &&
	test_line_count = 4 branch-version-count
'

for tiler_branch_kind in chain branch
do
	test_expect_success TILER,LINUX,SQUASHFS_PACK,WORKFLOW_GIT,FUSE_TOOLCHAIN "a fresh $tiler_branch_kind view inherits ancestors and excludes sibling work" '
		if test "$tiler_branch_kind" = chain
		then
			branch_id=$chain_tip_id &&
			other_branch=branch
		else
			branch_id=$branch_tip_id &&
			other_branch=chain
		fi &&
		(
			cd "$standard" &&
			"$tiler_bin" mount --rm "standard/app:$branch_id" "$verify_point" 2>"$HOME/$tiler_branch_kind-verify.err"
		) >"$tiler_branch_kind-verify.out" &&
		verify_session=$(sed -n "s/^new session created: //p" "$tiler_branch_kind-verify.out") &&
		test -n "$verify_session" &&
		test_cmp "$tiler_branch_kind-published" "$verify_point/$tiler_branch_kind.txt" &&
		test_cmp "$prepare/docs/guide.txt" "$verify_point/docs/guide.txt" &&
		test_path_is_missing "$verify_point/$other_branch.txt" &&
		test_path_is_missing "$verify_point/chain-pending.txt" &&
		test_path_is_missing "$verify_point/branch-pending.txt" &&
		test_path_is_missing "$verify_point/pending.txt" &&
		if test "$tiler_branch_kind" = chain
		then
			test_cmp delta-readme "$verify_point/README.txt" &&
			test_cmp delta-addition "$verify_point/published.txt" &&
			test_path_is_missing "$verify_point/obsolete.txt"
		else
			test_cmp "$prepare/README.txt" "$verify_point/README.txt" &&
			test_cmp "$prepare/obsolete.txt" "$verify_point/obsolete.txt" &&
			test_path_is_missing "$verify_point/published.txt"
		fi &&
		test_cmp "$tiler_branch_kind-pending" "$standard/mnt-$tiler_branch_kind/$tiler_branch_kind-pending.txt" &&
		(
			cd "$standard" &&
			"$tiler_bin" umount "$verify_point" 2>"$HOME/$tiler_branch_kind-verify-umount.err"
		) >"$tiler_branch_kind-verify-umount.out" &&
		find "$verify_point" -mindepth 1 -print >branch-verify-unmounted &&
		test_must_be_empty branch-verify-unmounted &&
		test_path_is_missing "$sessions/$verify_session" &&
		test_path_is_missing "$logs/sessions/$verify_session.log" &&
		test_path_is_missing "$logs/sessions/$verify_session.log.lock"
	'
done

test_done
