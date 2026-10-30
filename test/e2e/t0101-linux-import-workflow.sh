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

# The imports below share the closed source's full and incremental versions.
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN,WORKFLOW_GIT 'publish an isolated tagged external full and increment' '
	workflow_config "$remote" &&
	workflow_config "$local" &&
	mkdir -p "$prepare/docs" "$remote/mnt-edit" &&
	workflow_git_init "$prepare" &&
	printf "external base\n" >"$prepare/README.txt" &&
	printf "external guide\n" >"$prepare/docs/guide.txt" &&
	git -C "$prepare" add . &&
	git -C "$prepare" commit -m "Create external source" &&
	git -C "$prepare" tag -a -m "External base" v1 &&
	test_atexit '\''
		for tiler_project in "$remote" "$local"
		do
			(
				cd "$tiler_project" || exit
				for tiler_point in "$tiler_project"/mnt-*
				do
					"$tiler_bin" umount "$tiler_point" >/dev/null 2>&1 ||
					"$fusermount3_bin" -uz "$tiler_point" >/dev/null 2>&1 || true
				done
			)
		done
	'\'' &&
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
	printf "external increment\n" >"$remote/mnt-edit/delta.txt" &&
	git -C "$remote/mnt-edit" add delta.txt &&
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
		"$tiler_bin" session close source-edit
	) >source-close.out &&
	find "$remote/mnt-edit" -mindepth 1 >source-unmounted &&
	test_must_be_empty source-unmounted &&
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
		test_cmp "$prepare/README.txt" "$point/README.txt" &&
		test_cmp "$prepare/docs/guide.txt" "$point/docs/guide.txt" &&
		test_path_is_missing "$point/delta.txt" &&
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
		test_cmp "$prepare/README.txt" "$point/README.txt" &&
		test_cmp "$prepare/docs/guide.txt" "$point/docs/guide.txt" &&
		printf "external increment\n" >expect-delta &&
		test_cmp expect-delta "$point/delta.txt" &&
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
	cp "$prepare/README.txt" offline-readme &&
	cp "$prepare/docs/guide.txt" offline-guide &&
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
			test_cmp offline-readme "$point/README.txt" &&
			test_cmp offline-guide "$point/docs/guide.txt" &&
			test_path_is_missing "$point/local-only.txt" &&
			if test "$tiler_shape" = chain
			then
				test_cmp expect-delta "$point/delta.txt" &&
				git -C "$point" rev-parse "v2^{commit}" >offline-tag &&
				test_cmp source-delta-tag offline-tag
			else
				test_path_is_missing "$point/delta.txt" &&
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
		test_cmp offline-readme "$point/README.txt" &&
		test_cmp offline-guide "$point/docs/guide.txt" &&
		test_path_is_missing "$point/local-only.txt" &&
		if test "$tiler_shape" = chain
		then
			test_cmp expect-delta "$point/delta.txt"
		else
			test_path_is_missing "$point/delta.txt"
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

test_done
