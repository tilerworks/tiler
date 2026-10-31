#!/bin/sh

test_description='Linux import workflow preserves external image data'

# Git allocator instrumentation can abort the external squashfuse daemon.
TEST_NO_MALLOC_CHECK=1
. "$(dirname "$0")/lib-tiler.sh"
. "$TEST_DIRECTORY/lib-workflow.sh"

imports=$HOME/ws-import
remote=$imports/remote
local=$imports/local
prepare=$remote/prepare
source=$remote/.tiler/images/source/app

# Decode mount-table paths before matching the two projects' physical root.
import_mounts () (
	tiler_mount_root=$(cd "$imports" && pwd -P) &&
	TILER_TEST_IMPORTS="$tiler_mount_root" awk '
	BEGIN { root = ENVIRON["TILER_TEST_IMPORTS"] }
	{
		point = $5
		gsub(/\\040/, " ", point)
		gsub(/\\011/, "\t", point)
		gsub(/\\012/, "\n", point)
		gsub(/\\134/, sprintf("%c%c", 92, 92), point)
		if (point == root || index(point, root "/") == 1) print point
	}' /proc/self/mountinfo
)

cleanup_imports () (
	tiler_cleanup_failed=0
	for tiler_project in "$remote" "$local"
	do
		cd "$tiler_project" || return 1
		for tiler_point in "$tiler_project"/mnt-*
		do
			test "$tiler_point" = "$tiler_project/mnt-*" && continue
			if "$tiler_bin" umount "$tiler_point" >"$HOME/cleanup-tiler.out" 2>"$HOME/cleanup-tiler.err"
			then
				continue
			fi
			printf "tiler umount failed for %s; trying FUSE cleanup\n" "$tiler_point" >&2
			cat "$HOME/cleanup-tiler.err" >&2
			if ! "$fusermount3_bin" -uz "$tiler_point" >"$HOME/cleanup-fuse.out" 2>"$HOME/cleanup-fuse.err"
			then
				printf "FUSE cleanup also failed for %s\n" "$tiler_point" >&2
				cat "$HOME/cleanup-fuse.err" >&2
				tiler_cleanup_failed=1
			fi
		done
	done
	import_mounts >"$HOME/cleanup-mounts" || return 1
	if test -s "$HOME/cleanup-mounts"
	then
		printf "Mounts remain below %s after cleanup:\n" "$imports" >&2
		cat "$HOME/cleanup-mounts" >&2
		return 1
	fi
	test "$tiler_cleanup_failed" -eq 0
)

# Expected content stays outside remote so offline mounts can use it too.
check_import_view () (
	test_cmp "$HOME/source-guide" "$1/docs/guide.txt" &&
	if test "$2" = base
	then
		test_cmp "$HOME/source-readme" "$1/README.txt" &&
		test_cmp "$HOME/source-obsolete" "$1/obsolete.txt" &&
		test_cmp "$HOME/source-rename" "$1/rename.txt" &&
		test_cmp "$HOME/source-tree" "$1/tree/nested/file.txt" &&
		test_cmp "$HOME/source-recreate" "$1/recreate.txt" &&
		test_path_is_missing "$1/renamed.txt" &&
		test_path_is_missing "$1/delta.txt"
	else
		test_cmp "$HOME/expect-delta-readme" "$1/README.txt" &&
		test_cmp "$HOME/expect-delta" "$1/delta.txt" &&
		test_cmp "$HOME/source-rename" "$1/renamed.txt" &&
		test_cmp "$HOME/expect-recreated" "$1/recreate.txt" &&
		test_path_is_missing "$1/rename.txt" &&
		test_path_is_missing "$1/obsolete.txt" &&
		test_path_is_missing "$1/tree"
	fi &&
	if test "$2" = tip
	then
		test_cmp "$HOME/expect-tip" "$1/tip.txt"
	else
		test_path_is_missing "$1/tip.txt"
	fi &&
	test_path_is_missing "$1/sibling.txt"
)

# Hash source metadata and payloads without including mutable activity logs.
source_snapshot () (
	cd "$source" &&
	find . -type f \( -name meta.yaml -o -name image.yaml -o -name image.sqsh \) >"$1.paths" &&
	LC_ALL=C sort "$1.paths" >"$1.sorted" &&
	while IFS= read -r tiler_source_file
	do
		sha256sum "$tiler_source_file" || return 1
	done <"$1.sorted" >"$1"
)

# The lifecycle shares a full -> delta -> tip chain and a sibling of delta.
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN,WORKFLOW_GIT 'publish an isolated external chain and sibling with tagged path changes' '
	workflow_config "$remote" &&
	workflow_config "$local" &&
	mkdir -p "$prepare/docs" "$prepare/tree/nested" "$remote/mnt-edit" "$remote/mnt-branch" &&
	workflow_git_init "$prepare" &&
	printf "external base\n" >"$prepare/README.txt" &&
	printf "external guide\n" >"$prepare/docs/guide.txt" &&
	printf "obsolete source file\n" >"$prepare/obsolete.txt" &&
	printf "rename source\n" >"$prepare/rename.txt" &&
	printf "source tree\n" >"$prepare/tree/nested/file.txt" &&
	printf "original source file\n" >"$prepare/recreate.txt" &&
	cp "$prepare/README.txt" source-readme &&
	cp "$prepare/docs/guide.txt" source-guide &&
	cp "$prepare/obsolete.txt" source-obsolete &&
	cp "$prepare/rename.txt" source-rename &&
	cp "$prepare/tree/nested/file.txt" source-tree &&
	cp "$prepare/recreate.txt" source-recreate &&
	git -C "$prepare" add . &&
	git -C "$prepare" commit -m "Create external source" &&
	git -C "$prepare" tag -a -m "External base" v1 &&
	test_atexit cleanup_imports &&
	(
		cd "$prepare" &&
		"$tiler_bin" build -I source/app -f --format squashfs -m initial 2>"$HOME/source-full.err"
	) >source-full.out &&
	test_grep -E "^full source/app [0-9a-f]{64}$" source-full.out &&
	base_id=$(cut -d" " -f3 source-full.out) &&
	(
		cd "$remote" &&
		"$tiler_bin" session new source-edit &&
		"$tiler_bin" mount -s source-edit "source/app:$base_id" "$remote/mnt-edit"
	) >source-mount.out &&
	printf "external increment\n" >expect-delta &&
	printf "external README changed in the increment\n" >expect-delta-readme &&
	printf "recreated source with different content\n" >expect-recreated &&
	cp expect-delta "$remote/mnt-edit/delta.txt" &&
	cp expect-delta-readme "$remote/mnt-edit/README.txt" &&
	rm -r "$remote/mnt-edit/tree" &&
	rm "$remote/mnt-edit/obsolete.txt" "$remote/mnt-edit/recreate.txt" &&
	cp expect-recreated "$remote/mnt-edit/recreate.txt" &&
	mv "$remote/mnt-edit/rename.txt" "$remote/mnt-edit/renamed.txt" &&
	git -C "$remote/mnt-edit" add -A &&
	git -C "$remote/mnt-edit" commit -m "Add external increment" &&
	git -C "$remote/mnt-edit" tag -a -m "External increment" v2 &&
	git -C "$remote/mnt-edit" rev-parse "v2^{commit}" >source-delta-tag &&
	(
		cd "$remote/mnt-edit" &&
		"$tiler_bin" build -i --format squashfs 2>"$HOME/source-delta.err"
	) >source-delta.out &&
	test_grep -E "^incr source/app [0-9a-f]{64}$" source-delta.out &&
	delta_id=$(cut -d" " -f3 source-delta.out) &&
	(
		cd "$remote" &&
		"$tiler_bin" session close source-edit &&
		"$tiler_bin" session open source-edit
	) >source-delta-replay.out &&
	printf "external chain tip\n" >expect-tip &&
	cp expect-tip "$remote/mnt-edit/tip.txt" &&
	git -C "$remote/mnt-edit" add tip.txt &&
	git -C "$remote/mnt-edit" commit -m "Add external tip" &&
	git -C "$remote/mnt-edit" tag -a -m "External tip" v3 &&
	git -C "$remote/mnt-edit" rev-parse "v3^{commit}" >source-tip-tag &&
	(
		cd "$remote/mnt-edit" &&
		"$tiler_bin" build -i --format squashfs 2>"$HOME/source-tip.err"
	) >source-tip.out &&
	test_grep -E "^incr source/app [0-9a-f]{64}$" source-tip.out &&
	tip_id=$(cut -d" " -f3 source-tip.out) &&
	(
		cd "$remote" &&
		"$tiler_bin" session close source-edit
	) >source-close.out &&
	find "$remote/mnt-edit" -mindepth 1 >source-unmounted &&
	test_must_be_empty source-unmounted &&
	(
		cd "$remote" &&
		"$tiler_bin" session new source-branch &&
		"$tiler_bin" mount -s source-branch "source/app:$base_id" "$remote/mnt-branch"
	) >source-branch-mount.out &&
	printf "external sibling\n" >"$remote/mnt-branch/sibling.txt" &&
	git -C "$remote/mnt-branch" add sibling.txt &&
	git -C "$remote/mnt-branch" commit -m "Add external sibling" &&
	(
		cd "$remote/mnt-branch" &&
		"$tiler_bin" build -i --format squashfs 2>"$HOME/source-sibling.err"
	) >source-sibling.out &&
	test_grep -E "^incr source/app [0-9a-f]{64}$" source-sibling.out &&
	sibling_id=$(cut -d" " -f3 source-sibling.out) &&
	(
		cd "$remote" &&
		"$tiler_bin" session close source-branch
	) >source-branch-close.out &&
	find "$remote/mnt-branch" -mindepth 1 >source-branch-unmounted &&
	test_must_be_empty source-branch-unmounted &&
	source_snapshot "$HOME/source.before"
'

for tiler_import_mode in link copy
do
	test_expect_success TILER,LINUX,FUSE_TOOLCHAIN,WORKFLOW_GIT "$tiler_import_mode imports the full file and preserves a writable view on repeat" '
		if test "$tiler_import_mode" = link
		then
			lib_name=linked/file
		else
			lib_name=copied/file
		fi &&
		point=$local/mnt-$tiler_import_mode-file &&
		object=$local/.tiler/images/$lib_name/_objects/$base_id &&
		mkdir -p "$point" &&
		(
			cd "$local" &&
			"$tiler_bin" build -I "$lib_name" --$tiler_import_mode "$source/_objects/$base_id/image.sqsh" 2>"$HOME/import.err"
		) >import.out &&
		test_grep "^$tiler_import_mode $lib_name $base_id$" import.out &&
		test_path_is_file "$object/image.sqsh" &&
		! test -L "$object/image.sqsh" &&
		test_path_is_file "$object/image.yaml" &&
		for tiler_meta in "$local/.tiler/images/$lib_name/meta.yaml" "$object/image.yaml"
		do
			test_grep "^name: $lib_name$" "$tiler_meta" &&
			test_grep -E "^[[:space:]]*(- )?id: $base_id$" "$tiler_meta" &&
			test_grep -E "^[[:space:]]*kind: full$" "$tiler_meta" &&
			test_grep -E "^[[:space:]]*type: squashfs$" "$tiler_meta" &&
			test_grep -E "^[[:space:]]*sha256: $base_id$" "$tiler_meta" || return 1
		done &&
		if test "$tiler_import_mode" = link
		then
			printf "%s\n" "$source/_objects/$base_id/image.sqsh" >expect-location &&
			test_cmp expect-location "$object/image.sqsh"
		else
			test_cmp "$source/_objects/$base_id/image.sqsh" "$object/image.sqsh" &&
			sha256sum "$object/image.sqsh" >copy.sum &&
			copy_sum=$(cut -d" " -f1 copy.sum) &&
			test "$copy_sum" = "$base_id"
		fi &&
		cp "$local/.tiler/images/$lib_name/meta.yaml" import-meta.before &&
		cp "$object/image.yaml" import-object.before &&
		(
			cd "$local" &&
			"$tiler_bin" mount --rm "$lib_name" "$point" 2>"$HOME/import-mount.err"
		) >import-mount.out &&
		import_session=$(sed -n "s/^new session created: //p" import-mount.out) &&
		test -n "$import_session" &&
		check_import_view "$point" base &&
		import_mounts >live-mounts &&
		test_grep -F -x "$point" live-mounts &&
		git -C "$point" rev-parse "v1^{commit}" >import-tag &&
		git -C "$prepare" rev-parse HEAD >source-tag &&
		test_cmp source-tag import-tag &&
		printf "local upper only\n" >expect-local &&
		cp expect-local "$point/local-only.txt" &&
		(
			cd "$local" &&
			"$tiler_bin" build -I "$lib_name" --$tiler_import_mode "$source/_objects/$base_id/image.sqsh" 2>"$HOME/import-repeat.err" &&
			"$tiler_bin" mount "$lib_name" "$point" 2>"$HOME/import-repeat-mount.err"
		) >import-repeat.out &&
		test_grep "^no-op $lib_name $base_id$" import-repeat.out &&
		test_grep -F "already mounted $point" import-repeat.out &&
		test_cmp import-meta.before "$local/.tiler/images/$lib_name/meta.yaml" &&
		test_cmp import-object.before "$object/image.yaml" &&
		test_cmp expect-local "$point/local-only.txt" &&
		test_cmp expect-local "$local/.tiler/sessions/$import_session/runtime/m1/upper/local-only.txt" &&
		source_snapshot "$HOME/source.after" &&
		test_cmp source.before source.after &&
		(
			cd "$local" &&
			"$tiler_bin" umount "$point"
		) >import-unmount.out &&
		test_path_is_missing "$local/.tiler/sessions/$import_session" &&
		find "$point" -mindepth 1 >import-unmounted &&
		test_must_be_empty import-unmounted
	'
done

for tiler_import_mode in link copy
do
	test_expect_success TILER,LINUX,FUSE_TOOLCHAIN,WORKFLOW_GIT "$tiler_import_mode imports a selected increment with its complete ancestry" '
		if test "$tiler_import_mode" = link
		then
			lib_name=linked/chain
		else
			lib_name=copied/chain
		fi &&
		point=$local/mnt-$tiler_import_mode-chain &&
		repo=$local/.tiler/images/$lib_name &&
		mkdir -p "$point" &&
		(
			cd "$local" &&
			"$tiler_bin" build -I "$lib_name" --$tiler_import_mode "$source" --hash "$base_id" 2>"$HOME/chain-full.err"
		) >chain-full.out &&
		test_grep "^$tiler_import_mode $lib_name $base_id$" chain-full.out &&
		grep "^[[:space:]]*- id:" "$repo/meta.yaml" >chain-nodes &&
		test_line_count = 1 chain-nodes &&
		test_path_is_missing "$repo/_objects/$delta_id" &&
		(
			cd "$local" &&
			"$tiler_bin" build -I "$lib_name" --$tiler_import_mode "$source" --hash "$delta_id" 2>"$HOME/chain-delta.err"
		) >chain-delta.out &&
		printf "no-op %s %s\n%s %s %s\n" "$lib_name" "$base_id" "$tiler_import_mode" "$lib_name" "$delta_id" >expect-chain-import &&
		test_cmp expect-chain-import chain-delta.out &&
		grep "^[[:space:]]*- id:" "$repo/meta.yaml" >chain-nodes &&
		test_line_count = 2 chain-nodes &&
		test_path_is_missing "$repo/_objects/$tip_id" &&
		test_path_is_missing "$repo/_objects/$sibling_id" &&
		for tiler_node in "$base_id" "$delta_id"
		do
			object=$repo/_objects/$tiler_node &&
			test_grep "^[[:space:]]*- id: $tiler_node$" "$repo/meta.yaml" &&
			test_path_is_file "$object/image.sqsh" &&
			! test -L "$object/image.sqsh" &&
			sed "s|^name: source/app$|name: $lib_name|" "$source/_objects/$tiler_node/image.yaml" >expect-chain-node &&
			test_cmp expect-chain-node "$object/image.yaml" &&
			cp "$object/image.yaml" "chain-$tiler_node.before" &&
			if test "$tiler_import_mode" = link
			then
				printf "%s\n" "$source/_objects/$tiler_node/image.sqsh" >expect-chain-location &&
				test_cmp expect-chain-location "$object/image.sqsh"
			else
				test_cmp "$source/_objects/$tiler_node/image.sqsh" "$object/image.sqsh" &&
				sha256sum "$object/image.sqsh" >chain-copy.sum &&
				chain_sum=$(cut -d" " -f1 chain-copy.sum) &&
				test "$chain_sum" = "$tiler_node"
			fi || return 1
		done &&
		(
			cd "$local" &&
			"$tiler_bin" images show "$lib_name:$delta_id" 2>"$HOME/chain-show.err"
		) >chain-show.out &&
		test_grep -E "^  id +$delta_id$" chain-show.out &&
		test_grep -E "^  parent +$base_id$" chain-show.out &&
		cp "$repo/meta.yaml" chain-meta.before &&
		(
			cd "$local" &&
			"$tiler_bin" mount --rm "$lib_name" "$point" 2>"$HOME/chain-mount.err"
		) >chain-mount.out &&
		chain_session=$(sed -n "s/^new session created: //p" chain-mount.out) &&
		test -n "$chain_session" &&
		check_import_view "$point" delta &&
		git -C "$point" rev-parse "v2^{commit}" >chain-tag &&
		test_cmp source-delta-tag chain-tag &&
		printf "unpublished chain upper\n" >expect-chain-upper &&
		cp expect-chain-upper "$point/local-only.txt" &&
		(
			cd "$local" &&
			"$tiler_bin" build -I "$lib_name" --$tiler_import_mode "$source" --hash "$delta_id" 2>"$HOME/chain-repeat.err"
		) >chain-repeat.out &&
		printf "no-op %s %s\nno-op %s %s\n" "$lib_name" "$base_id" "$lib_name" "$delta_id" >expect-chain-repeat &&
		test_cmp expect-chain-repeat chain-repeat.out &&
		test_cmp chain-meta.before "$repo/meta.yaml" &&
		for tiler_node in "$base_id" "$delta_id"
		do
			test_cmp "chain-$tiler_node.before" "$repo/_objects/$tiler_node/image.yaml" || return 1
		done &&
		(
			cd "$local" &&
			"$tiler_bin" mount "$lib_name" "$point" 2>"$HOME/chain-repeat-mount.err"
		) >chain-repeat-mount.out &&
		test_grep -F "already mounted $point" chain-repeat-mount.out &&
		test_cmp expect-delta "$point/delta.txt" &&
		test_cmp expect-chain-upper "$point/local-only.txt" &&
		test_cmp expect-chain-upper "$local/.tiler/sessions/$chain_session/runtime/m1/upper/local-only.txt" &&
		source_snapshot "$HOME/source.after" &&
		test_cmp source.before source.after &&
		(
			cd "$local" &&
			"$tiler_bin" umount "$point"
		) >chain-unmount.out &&
		test_path_is_missing "$local/.tiler/sessions/$chain_session" &&
		find "$point" -mindepth 1 >chain-unmounted &&
		test_must_be_empty chain-unmounted
	'
done

# A payload alone has no directory-level ancestry contract, even if its sidecar
# describes an incremental object. --hash is reserved for directory imports.
for tiler_import_mode in link copy
do
	test_expect_success TILER,LINUX,FUSE_TOOLCHAIN,WORKFLOW_GIT "$tiler_import_mode treats an incremental payload file as a standalone full image" '
		lib_name=boundary/$tiler_import_mode &&
		repo=$local/.tiler/images/$lib_name &&
		(
			cd "$local" &&
			"$tiler_bin" build -I "$lib_name" --$tiler_import_mode "$source/_objects/$delta_id/image.sqsh" 2>"$HOME/boundary-file.err"
		) >boundary-file.out &&
		test_grep "^$tiler_import_mode $lib_name $delta_id$" boundary-file.out &&
		grep "^[[:space:]]*- id:" "$repo/meta.yaml" >boundary-nodes &&
		test_line_count = 1 boundary-nodes &&
		test_grep -E "^[[:space:]]*kind: full$" "$repo/_objects/$delta_id/image.yaml" &&
		test_grep ! -E "^[[:space:]]*parent:" "$repo/_objects/$delta_id/image.yaml" &&
		test_path_is_missing "$repo/_objects/$base_id" &&
		cp "$repo/meta.yaml" boundary-meta.before &&
		(
			cd "$local" &&
			test_expect_code 2 "$tiler_bin" build -I "$lib_name" --$tiler_import_mode "$source/_objects/$delta_id/image.sqsh" --hash "$delta_id" >"$HOME/boundary-hash.out" 2>"$HOME/boundary-hash.err"
		) &&
		test_must_be_empty boundary-hash.out &&
		test_grep -F -- "--hash applies only to an image directory" boundary-hash.err &&
		test_cmp boundary-meta.before "$repo/meta.yaml" &&
		(
			cd "$local" &&
			"$tiler_bin" images remove "$lib_name"
		) >boundary-remove.out &&
		test_path_is_missing "$repo"
	'

	test_expect_success TILER,LINUX,FUSE_TOOLCHAIN,WORKFLOW_GIT "$tiler_import_mode imports a fresh tip with all ancestors and excludes its sibling" '
		if test "$tiler_import_mode" = link
		then
			lib_name=linked/fresh
		else
			lib_name=copied/fresh
		fi &&
		repo=$local/.tiler/images/$lib_name &&
		point=$local/mnt-$tiler_import_mode-fresh &&
		mkdir -p "$point" &&
		test_path_is_missing "$repo" &&
		(
			cd "$local" &&
			"$tiler_bin" build -I "$lib_name" --$tiler_import_mode "$source" --hash "$tip_id" 2>"$HOME/fresh-import.err"
		) >fresh-import.out &&
		printf "%s %s %s\n%s %s %s\n%s %s %s\n" \
			"$tiler_import_mode" "$lib_name" "$base_id" \
			"$tiler_import_mode" "$lib_name" "$delta_id" \
			"$tiler_import_mode" "$lib_name" "$tip_id" >expect-fresh-import &&
		test_cmp expect-fresh-import fresh-import.out &&
		grep "^[[:space:]]*- id:" "$repo/meta.yaml" >fresh-nodes &&
		test_line_count = 3 fresh-nodes &&
		test_path_is_missing "$repo/_objects/$sibling_id" &&
		for tiler_node in "$base_id" "$delta_id" "$tip_id"
		do
			object=$repo/_objects/$tiler_node &&
			sed "s|^name: source/app$|name: $lib_name|" "$source/_objects/$tiler_node/image.yaml" >expect-fresh-node &&
			test_cmp expect-fresh-node "$object/image.yaml" &&
			if test "$tiler_import_mode" = link
			then
				printf "%s\n" "$source/_objects/$tiler_node/image.sqsh" >expect-fresh-location &&
				test_cmp expect-fresh-location "$object/image.sqsh"
			else
				test_cmp "$source/_objects/$tiler_node/image.sqsh" "$object/image.sqsh" &&
				sha256sum "$object/image.sqsh" >fresh-copy.sum &&
				fresh_sum=$(cut -d" " -f1 fresh-copy.sum) &&
				test "$fresh_sum" = "$tiler_node"
			fi || return 1
		done &&
		(
			cd "$local" &&
			"$tiler_bin" mount --rm "$lib_name" "$point" 2>"$HOME/fresh-mount.err"
		) >fresh-mount.out &&
		fresh_session=$(sed -n "s/^new session created: //p" fresh-mount.out) &&
		test -n "$fresh_session" &&
		check_import_view "$point" tip &&
		git -C "$point" rev-parse "v3^{commit}" >fresh-tag &&
		test_cmp source-tip-tag fresh-tag &&
		(
			cd "$local" &&
			"$tiler_bin" umount "$point" &&
			"$tiler_bin" images remove "$lib_name"
		) >fresh-remove.out &&
		test_path_is_missing "$local/.tiler/sessions/$fresh_session" &&
		test_path_is_missing "$repo" &&
		source_snapshot "$HOME/source.after-fresh" &&
		test_cmp source.before source.after-fresh
	'
done

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN,WORKFLOW_GIT 'take the source offline after every imported view is destroyed' '
	for tiler_point in "$local"/mnt-*
	do
		find "$tiler_point" -mindepth 1 >offline-unmounted &&
		test_must_be_empty offline-unmounted || return 1
	done &&
	find "$local/.tiler/sessions" -name session.json >offline-records &&
	test_must_be_empty offline-records &&
	find "$local/.tiler/sessions" -name runtime >offline-runtime &&
	test_must_be_empty offline-runtime &&
	rm -rf "$local/.tiler/cache" &&
	test_path_is_missing "$local/.tiler/cache" &&
	test_atexit '\''
		if test -d "$imports/remote-offline"
		then
			mv "$imports/remote-offline" "$remote"
		fi
	'\'' &&
	mv "$remote" "$imports/remote-offline" &&
	test_path_is_missing "$remote"
'

for tiler_import_name in linked/file linked/chain copied/file copied/chain
do
	test_expect_success TILER,LINUX,FUSE_TOOLCHAIN,WORKFLOW_GIT "$tiler_import_name has the expected source availability requirement" '
		tiler_shape=${tiler_import_name#*/} &&
		if test "${tiler_import_name%%/*}" = linked
		then
			point=$local/mnt-link-$tiler_shape &&
			(
				cd "$local" &&
				test_expect_code 1 "$tiler_bin" mount --rm "$tiler_import_name" "$point" >"$HOME/offline-link.out" 2>"$HOME/offline-link.err"
			) &&
			test_must_be_empty offline-link.out &&
			test_grep -F "$source" offline-link.err &&
			find "$local/.tiler/sessions" -name session.json >offline-failed-records &&
			test_must_be_empty offline-failed-records
		else
			point=$local/mnt-copy-$tiler_shape &&
			(
				cd "$local" &&
				"$tiler_bin" mount --rm "$tiler_import_name" "$point" 2>"$HOME/offline-copy.err"
			) >offline-copy.out &&
			fresh_session=$(sed -n "s/^new session created: //p" offline-copy.out) &&
			test -n "$fresh_session" &&
			test_path_is_missing "$point/local-only.txt" &&
			if test "$tiler_shape" = chain
			then
				check_import_view "$point" delta &&
				git -C "$point" rev-parse "v2^{commit}" >offline-tag &&
				test_cmp source-delta-tag offline-tag
			else
				check_import_view "$point" base &&
				git -C "$point" rev-parse "v1^{commit}" >offline-tag &&
				test_cmp source-tag offline-tag
			fi &&
			(
				cd "$local" &&
				"$tiler_bin" umount "$point"
			) >offline-copy-unmount.out &&
			test_path_is_missing "$local/.tiler/sessions/$fresh_session"
		fi &&
		find "$point" -mindepth 1 >offline-empty &&
		test_must_be_empty offline-empty
	'
done

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN,WORKFLOW_GIT 'restore the external source without changing its metadata or payloads' '
	mv "$imports/remote-offline" "$remote" &&
	source_snapshot "$HOME/source.restored" &&
	test_cmp source.before source.restored
'

for tiler_missing_layer in full incremental
do
	test_expect_success TILER,LINUX,FUSE_TOOLCHAIN,WORKFLOW_GIT "a missing $tiler_missing_layer payload blocks links while copies remain usable" '
		if test "$tiler_missing_layer" = full
		then
			tiler_missing_id=$base_id
		else
			tiler_missing_id=$delta_id
		fi &&
		tiler_missing_payload=$source/_objects/$tiler_missing_id/image.sqsh &&
		import_mounts >fault-before-mounts &&
		test_must_be_empty fault-before-mounts &&
		rm -rf "$local/.tiler/cache" &&
		test_when_finished '\''
			if test -f "$HOME/withheld.sqsh"
			then
				mv "$HOME/withheld.sqsh" "$tiler_missing_payload"
			fi
		'\'' &&
		mv "$tiler_missing_payload" "$HOME/withheld.sqsh" &&
		(
			cd "$local" &&
			test_expect_code 1 "$tiler_bin" mount --rm linked/chain "$local/mnt-link-chain" >"$HOME/missing-link.out" 2>"$HOME/missing-link.err"
		) &&
		test_must_be_empty missing-link.out &&
		test_grep -F "$tiler_missing_payload" missing-link.err &&
		find "$local/.tiler/sessions" -name session.json >missing-link-records &&
		test_must_be_empty missing-link-records &&
		(
			cd "$local" &&
			"$tiler_bin" mount --rm copied/chain "$local/mnt-copy-chain" 2>"$HOME/missing-copy.err"
		) >missing-copy.out &&
		check_import_view "$local/mnt-copy-chain" delta &&
		(
			cd "$local" &&
			"$tiler_bin" umount "$local/mnt-copy-chain"
		) >missing-copy-unmount.out &&
		mv "$HOME/withheld.sqsh" "$tiler_missing_payload" &&
		(
			cd "$local" &&
			"$tiler_bin" mount --rm linked/chain "$local/mnt-link-chain" 2>"$HOME/missing-restored.err"
		) >missing-restored.out &&
		check_import_view "$local/mnt-link-chain" delta &&
		(
			cd "$local" &&
			"$tiler_bin" umount "$local/mnt-link-chain"
		) >missing-restored-unmount.out &&
		source_snapshot "$HOME/source.after-missing" &&
		test_cmp source.before source.after-missing
	'
done

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN,WORKFLOW_GIT 'verified links reject same-size same-mtime corruption while copies remain intact' '
	import_mounts >corrupt-before-mounts &&
	test_must_be_empty corrupt-before-mounts &&
	tiler_corrupt_payload=$source/_objects/$base_id/image.sqsh &&
	cp -p "$tiler_corrupt_payload" "$HOME/payload.before" &&
	test_when_finished '\''
		cat "$HOME/payload.before" >"$tiler_corrupt_payload" &&
		touch -r "$HOME/payload.before" "$tiler_corrupt_payload"
	'\'' &&
	dd if="$tiler_corrupt_payload" of=original-byte bs=1 skip=128 count=1 status=none &&
	od -An -tu1 original-byte >original-byte.value &&
	read -r tiler_original_byte <original-byte.value &&
	tiler_replacement_byte=$((tiler_original_byte ^ 1)) &&
	tiler_octal=$(printf "%03o" "$tiler_replacement_byte") &&
	printf "%b" "\\$tiler_octal" >replacement-byte &&
	dd if=replacement-byte of="$tiler_corrupt_payload" bs=1 seek=128 conv=notrunc status=none &&
	touch -r "$HOME/payload.before" "$tiler_corrupt_payload" &&
	stat -c "%s %y" "$HOME/payload.before" >payload-stat.before &&
	stat -c "%s %y" "$tiler_corrupt_payload" >payload-stat.after &&
	test_cmp payload-stat.before payload-stat.after &&
	sha256sum "$tiler_corrupt_payload" >corrupt.sum &&
	corrupt_sum=$(cut -d" " -f1 corrupt.sum) &&
	test "$corrupt_sum" != "$base_id" &&
	rm -rf "$local/.tiler/cache" &&
	for tiler_shape in file chain
	do
		(
			cd "$local" &&
			test_expect_code 1 "$tiler_bin" mount --verify --rm "linked/$tiler_shape" "$local/mnt-link-$tiler_shape" >"$HOME/corrupt-link.out" 2>"$HOME/corrupt-link.err"
		) &&
		test_must_be_empty corrupt-link.out &&
		test_grep -F "$local/.tiler/images/linked/$tiler_shape/_objects/$base_id/image.sqsh" corrupt-link.err &&
		test_grep -F sha256 corrupt-link.err &&
		(
			cd "$local" &&
			"$tiler_bin" mount --verify --rm "copied/$tiler_shape" "$local/mnt-copy-$tiler_shape" 2>"$HOME/corrupt-copy.err"
		) >corrupt-copy.out &&
		if test "$tiler_shape" = chain
		then
			check_import_view "$local/mnt-copy-$tiler_shape" delta
		else
			check_import_view "$local/mnt-copy-$tiler_shape" base
		fi &&
		(
			cd "$local" &&
			"$tiler_bin" umount "$local/mnt-copy-$tiler_shape"
		) >corrupt-copy-unmount.out || return 1
	done &&
	find "$local/.tiler/sessions" -name session.json >corrupt-records &&
	test_must_be_empty corrupt-records &&
	cat "$HOME/payload.before" >"$tiler_corrupt_payload" &&
	touch -r "$HOME/payload.before" "$tiler_corrupt_payload" &&
	source_snapshot "$HOME/source.after-corrupt" &&
	test_cmp source.before source.after-corrupt
'

for tiler_import_name in linked/file linked/chain copied/file copied/chain
do
	test_expect_success TILER,LINUX,FUSE_TOOLCHAIN,WORKFLOW_GIT "$tiler_import_name mounts after source recovery and removes without touching the source" '
		tiler_shape=${tiler_import_name#*/} &&
		if test "${tiler_import_name%%/*}" = linked
		then
			point=$local/mnt-link-$tiler_shape
		else
			point=$local/mnt-copy-$tiler_shape
		fi &&
		(
			cd "$local" &&
			"$tiler_bin" mount --rm "$tiler_import_name" "$point" 2>"$HOME/recovery-mount.err"
		) >recovery-mount.out &&
		fresh_session=$(sed -n "s/^new session created: //p" recovery-mount.out) &&
		test -n "$fresh_session" &&
		test_path_is_missing "$point/local-only.txt" &&
		if test "$tiler_shape" = chain
		then
			check_import_view "$point" delta
		else
			check_import_view "$point" base
		fi &&
		(
			cd "$local" &&
			"$tiler_bin" umount "$point" &&
			"$tiler_bin" images remove "$tiler_import_name"
		) >recovery-remove.out &&
		test_grep -F "Removed image repository $tiler_import_name" recovery-remove.out &&
		test_path_is_missing "$local/.tiler/sessions/$fresh_session" &&
		test_path_is_missing "$local/.tiler/images/$tiler_import_name" &&
		test_path_is_missing "$local/.tiler/logs/images/$tiler_import_name" &&
		find "$point" -mindepth 1 >recovery-empty &&
		test_must_be_empty recovery-empty &&
		source_snapshot "$HOME/source.after-remove" &&
		test_cmp source.before source.after-remove
	'
done

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN,WORKFLOW_GIT 'the import lifecycle leaves no kernel mounts or local session runtime' '
	import_mounts >final-mounts &&
	test_must_be_empty final-mounts &&
	find "$local/.tiler/sessions" -name session.json -o -name runtime >final-session-data &&
	test_must_be_empty final-session-data
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN,WORKFLOW_GIT 'import cleanup reports failures from both unmount tools' '
	cat >"$HOME/fail-unmount" <<-\EOF &&
	#!/bin/sh
	printf "injected unmount failure\n" >&2
	exit 1
	EOF
	chmod +x "$HOME/fail-unmount" &&
	(
		tiler_bin=$HOME/fail-unmount &&
		fusermount3_bin=$HOME/fail-unmount &&
		test_expect_code 1 cleanup_imports >"$HOME/cleanup-probe.out" 2>"$HOME/cleanup-probe.err"
	) &&
	test_grep "^tiler umount failed for " cleanup-probe.err &&
	test_grep "^FUSE cleanup also failed for " cleanup-probe.err &&
	test_grep "^injected unmount failure$" cleanup-probe.err &&
	import_mounts >cleanup-probe-mounts &&
	test_must_be_empty cleanup-probe-mounts
'

test_done
