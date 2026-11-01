#!/bin/sh

test_description='Linux replay preserves post-publication deletions and recovery'

# Git allocator instrumentation can abort the external squashfuse daemon.
TEST_NO_MALLOC_CHECK=1
. "$(dirname "$0")/lib-tiler.sh"
. "$TEST_DIRECTORY/lib-workflow.sh"

replay=$HOME/ws-replay
view=$replay/mnt-edit
verify=$replay/mnt-verify
sessions=$replay/.tiler/sessions

cleanup_replay () (
	cd "$replay" || return 1
	for tiler_point in "$view" "$verify"
	do
		"$tiler_bin" umount "$tiler_point" || "$fusermount3_bin" -uz "$tiler_point" || return 1
	done
)

# These cases share one library; each scenario owns a separate persistent upper.
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'prepare a library without Git metadata in the upper' '
	workflow_config "$replay" &&
	mkdir -p "$replay/prepare" "$view" "$verify" &&
	printf "base content\n" >"$replay/prepare/anchor" &&
	test_atexit cleanup_replay &&
	(
		cd "$replay/prepare" &&
		"$tiler_bin" build -I replay/app -f --format squashfs 2>"$HOME/replay-base.err"
	) >replay-base.out &&
	base_id=$(cut -d" " -f3 replay-base.out) &&
	test_grep -E "^full replay/app [0-9a-f]{64}$" replay-base.out &&
	printf "published\n" >published &&
	printf "recreated after publication\n" >recreated
'

for tiler_publication in incremental full
do
	test_expect_success TILER,LINUX,FUSE_TOOLCHAIN "two $tiler_publication publications retain later path operations" '
		(
			cd "$replay" &&
			"$tiler_bin" session new "$tiler_publication" &&
			"$tiler_bin" mount -s "$tiler_publication" "replay/app:$base_id" "$view"
		) >replay-mount.out 2>replay-mount.err &&
		mkdir -p "$view/published-tree/nested" &&
		cp published "$view/published-tree/nested/child" &&
		cp published "$view/published-file" &&
		cp published "$view/rename-source" &&
		cp published "$view/recreate" &&
		ln -s anchor "$view/published-link" &&
		if test "$tiler_publication" = full
		then
			tiler_build_flag=-f
		else
			tiler_build_flag=-i
		fi &&
		(
			cd "$view" &&
			"$tiler_bin" build "$tiler_build_flag" 2>"$HOME/replay-first.err"
		) >replay-first.out &&
		cp published "$view/second-publication" &&
		(
			cd "$view" &&
			"$tiler_bin" build "$tiler_build_flag" 2>"$HOME/replay-second.err"
		) >replay-second.out &&
		tip_id=$(cut -d" " -f3 replay-second.out) &&
		test_grep -E "^(full|incr) replay/app [0-9a-f]{64}$" replay-second.out &&
		rm -r "$view/published-tree" &&
		rm "$view/published-file" "$view/second-publication" "$view/published-link" "$view/recreate" &&
		cp recreated "$view/recreate" &&
		mv "$view/rename-source" "$view/rename-destination" &&
		(
			cd "$replay" &&
			"$tiler_bin" umount "$view" &&
			"$tiler_bin" session open "$tiler_publication"
		) >replay-open.out 2>replay-open.err &&
		for tiler_deleted in published-tree published-file second-publication published-link rename-source
		do
			test_path_is_missing "$view/$tiler_deleted" || return 1
		done &&
		test_cmp recreated "$view/recreate" &&
		test_cmp published "$view/rename-destination" &&
		test_cmp "$replay/prepare/anchor" "$view/anchor" &&
		test_path_is_missing "$sessions/$tiler_publication/runtime/m1/rebase.json" &&
		(
			cd "$replay" &&
			"$tiler_bin" mount --rm "replay/app:$tip_id" "$verify"
		) >replay-verify.out 2>replay-verify.err &&
		test_cmp published "$verify/published-file" &&
		test_cmp published "$verify/second-publication" &&
		test_cmp published "$verify/published-tree/nested/child" &&
		test_cmp published "$verify/rename-source" &&
		test_cmp published "$verify/recreate" &&
		tiler_link=$(readlink "$verify/published-link") &&
		test "$tiler_link" = anchor &&
		(
			cd "$replay" &&
			"$tiler_bin" umount "$verify" &&
			"$tiler_bin" session close "$tiler_publication" &&
			"$tiler_bin" session open "$tiler_publication"
		) >replay-repeat.out 2>replay-repeat.err &&
		for tiler_deleted in published-tree published-file second-publication published-link rename-source
		do
			test_path_is_missing "$view/$tiler_deleted" || return 1
		done &&
		test_cmp recreated "$view/recreate" &&
		test_cmp published "$view/rename-destination" &&
		(
			cd "$replay" &&
			"$tiler_bin" session close "$tiler_publication"
		) >replay-close.out 2>replay-close.err
	'
done

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'deleting the only upper file still reconciles an empty upper' '
	(
		cd "$replay" &&
		"$tiler_bin" session new empty-upper &&
		"$tiler_bin" mount -s empty-upper "replay/app:$base_id" "$view"
	) >empty-mount.out 2>empty-mount.err &&
	cp published "$view/only-upper-file" &&
	(
		cd "$view" &&
		"$tiler_bin" build -i 2>"$HOME/empty-build.err"
	) >empty-build.out &&
	rm "$view/only-upper-file" &&
	(
		cd "$replay" &&
		"$tiler_bin" umount "$view"
	) >empty-umount.out 2>empty-umount.err &&
	find "$sessions/empty-upper/runtime/m1/upper" -mindepth 1 -print >empty-upper &&
	test_must_be_empty empty-upper &&
	(
		cd "$replay" &&
		"$tiler_bin" session open empty-upper
	) >empty-open.out 2>empty-open.err &&
	test_path_is_missing "$view/only-upper-file" &&
	test_cmp "$replay/prepare/anchor" "$view/anchor" &&
	(
		cd "$replay" &&
		"$tiler_bin" session close empty-upper
	) >empty-close.out 2>empty-close.err
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'prepare a replay with both redundant and unpublished upper files' '
	(
		cd "$replay" &&
		"$tiler_bin" session new recovery &&
		"$tiler_bin" mount -s recovery "replay/app:$base_id" "$view"
	) >recovery-mount.out 2>recovery-mount.err &&
	cp published "$view/deleted" &&
	cp published "$view/baked" &&
	(
		cd "$view" &&
		"$tiler_bin" build -i 2>"$HOME/recovery-build.err"
	) >recovery-build.out &&
	rm "$view/deleted" &&
	cp recreated "$view/later" &&
	(
		cd "$replay" &&
		"$tiler_bin" umount "$view"
	) >recovery-umount.out 2>recovery-umount.err &&
	recovery_runtime=$sessions/recovery/runtime/m1 &&
	real_overlay=$(command -v fuse-overlayfs) &&
	mkdir fault-bin &&
	cat >fault-bin/fuse-overlayfs <<-\EOF &&
	#!/bin/sh
	for point
	do
		:
	done
	if test "$point" = "$TILER_REBASE_FAULT_POINT"
	then
		echo "injected replay failure" >&2
		exit 1
	fi
	exec "$TILER_REAL_OVERLAY" "$@"
	EOF
	chmod +x fault-bin/fuse-overlayfs
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'application failure keeps a durable unapplied plan and leaves upper data intact' '
	(
		cd "$replay" &&
		test_must_fail env PATH="$HOME/fault-bin:$PATH" \
			TILER_REAL_OVERLAY="$real_overlay" \
			TILER_REBASE_FAULT_POINT="$recovery_runtime/rebase/apply" \
			"$tiler_bin" session open recovery 2>"$HOME/recovery-apply.err"
	) >recovery-apply.out &&
	test_grep -F "injected replay failure" recovery-apply.err &&
	test_grep -F "\"deleted\":[\"deleted\"]" "$recovery_runtime/rebase.json" &&
	test_grep -F "\"applied\":false" "$recovery_runtime/rebase.json" &&
	test_cmp published "$recovery_runtime/upper/baked" &&
	test_cmp recreated "$recovery_runtime/upper/later" &&
	find "$view" -mindepth 1 -print >failed-view &&
	test_must_be_empty failed-view
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'final mount failure retains the applied plan after redundant files are pruned' '
	(
		cd "$replay" &&
		test_must_fail env PATH="$HOME/fault-bin:$PATH" \
			TILER_REAL_OVERLAY="$real_overlay" TILER_REBASE_FAULT_POINT="$view" \
			"$tiler_bin" session open recovery 2>"$HOME/recovery-final.err"
	) >recovery-final.out &&
	test_grep -F "injected replay failure" recovery-final.err &&
	test_grep -F "\"applied\":true" "$recovery_runtime/rebase.json" &&
	test_grep -F "\"ready\":true" "$recovery_runtime/rebase.json" &&
	test_path_is_missing "$recovery_runtime/upper/baked" &&
	test_cmp recreated "$recovery_runtime/upper/later" &&
	find "$view" -mindepth 1 -print >failed-view &&
	test_must_be_empty failed-view
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'retry reuses the saved plan and does not reinterpret pruned files as deletions' '
	(
		cd "$replay" &&
		env PATH="$HOME/fault-bin:$PATH" TILER_REAL_OVERLAY="$real_overlay" \
			TILER_REBASE_FAULT_POINT="$recovery_runtime/rebase/apply" \
			"$tiler_bin" session open recovery 2>"$HOME/recovery-retry.err"
	) >recovery-retry.out &&
	test_path_is_missing "$view/deleted" &&
	test_cmp published "$view/baked" &&
	test_cmp recreated "$view/later" &&
	test_path_is_missing "$recovery_runtime/rebase.json" &&
	test_grep ! -F "\"served_tip\":" "$sessions/recovery/session.json" &&
	(
		cd "$replay" &&
		"$tiler_bin" session close recovery
	) >recovery-close.out 2>recovery-close.err
'

for tiler_private_mode in 0200 0000
do
	test_expect_success TILER,LINUX,FUSE_TOOLCHAIN "replay preserves an unrelated upper file with mode $tiler_private_mode" '
		(
			cd "$replay" &&
			"$tiler_bin" session new "private-$tiler_private_mode"
		) >private-new.out 2>private-new.err &&
		test_when_finished "(
			cd \"\$replay\" &&
			\"\$tiler_bin\" session close private-$tiler_private_mode
		)" &&
		(
			cd "$replay" &&
			"$tiler_bin" mount -s "private-$tiler_private_mode" "replay/app:$base_id" "$view"
		) >private-mount.out 2>private-mount.err &&
		cp published "$view/deleted" &&
		(
			cd "$view" &&
			"$tiler_bin" build -i 2>"$HOME/private-build.err"
		) >private-build.out &&
		rm "$view/deleted" &&
		cp recreated "$view/private" &&
		chmod "$tiler_private_mode" "$view/private" &&
		stat -c %a "$view/private" >private-mode-before &&
		(
			cd "$replay" &&
			"$tiler_bin" umount "$view" &&
			"$tiler_bin" session open "private-$tiler_private_mode" 2>"$HOME/private-open.err"
		) >private-open.out &&
		test_path_is_missing "$view/deleted" &&
		stat -c %a "$view/private" >private-mode-after &&
		test_cmp private-mode-before private-mode-after &&
		test_path_is_missing "$sessions/private-$tiler_private_mode/runtime/m1/rebase.json" &&
		chmod 600 "$view/private" &&
		test_cmp recreated "$view/private"
	'
done

test_done
