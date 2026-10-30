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

test_done
