#!/bin/sh

test_description='Linux split workflow verifies configured block payloads'

. "$(dirname "$0")/lib-tiler.sh"
. "$TEST_DIRECTORY/lib-workflow.sh"

split=$HOME/ws-split
prepare=$split/prepare
images=$split/.tiler/images/split/app
sessions=$split/.tiler/sessions
edit=$split/mnt-edit
verify=$split/mnt-verify
cache=$split/.tiler/cache

test_lazy_prereq SPLIT_CONCATFS '
	command -v concatfs >/dev/null 2>&1
'

split_mounts () (
	tiler_mount_root=$(cd "$split" && pwd -P) &&
	TILER_TEST_SPLIT="$tiler_mount_root" awk '
	BEGIN { root = ENVIRON["TILER_TEST_SPLIT"] }
	{
		point = $5
		gsub(/\\040/, " ", point)
		gsub(/\\011/, "\t", point)
		gsub(/\\012/, "\n", point)
		gsub(/\\134/, sprintf("%c%c", 92, 92), point)
		if (point == root || index(point, root "/") == 1) print point
	}' /proc/self/mountinfo
)

cleanup_split () (
	cd "$split" || return 1
	tiler_cleanup_failed=0
	for tiler_point in "$edit" "$verify"
	do
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
	split_mounts >"$HOME/cleanup-mounts" || return 1
	if test -s "$HOME/cleanup-mounts"
	then
		printf "Mounts remain below %s after cleanup:\n" "$split" >&2
		cat "$HOME/cleanup-mounts" >&2
		return 1
	fi
	test "$tiler_cleanup_failed" -eq 0
)

check_split_view () {
	for tiler_binary in base-a base-b
	do
		test_cmp "$prepare/bin/$tiler_binary.bin" "$1/bin/$tiler_binary.bin" || return 1
	done &&
	test_cmp "$HOME/delta-expected.bin" "$1/bin/delta.bin" &&
	test_cmp "$HOME/published-note" "$1/note.txt" &&
	test_path_is_missing "$1/obsolete.txt" &&
	test_path_is_missing "$1/pending.txt" &&
	test_path_is_file "$1/published-delete.txt"
}

# Packing checks stay available without overlay tools or a FUSE device.
test_expect_success TILER,LINUX,SQUASHFS_PACK,WORKFLOW_SQUASHFS_CLIENT 'prepare a project with one MiB blocks and incompressible data' '
	workflow_config "$split" &&
	printf "split_size: 1M\n" >>"$split/.tiler.yaml" &&
	mkdir -p "$prepare/bin" &&
	printf "original note\n" >"$prepare/note.txt" &&
	printf "obsolete file\n" >"$prepare/obsolete.txt" &&
	for tiler_binary in base-a base-b
	do
		dd if=/dev/urandom of="$prepare/bin/$tiler_binary.bin" bs=1048576 count=2 status=none || return 1
	done
'

test_expect_success TILER,LINUX,SQUASHFS_PACK,WORKFLOW_SQUASHFS_CLIENT 'project configuration splits a full squashfs without a split flag' '
	(
		cd "$prepare" &&
		"$tiler_bin" build -I split/app -f --format squashfs 2>"$HOME/split-full.err"
	) >split-full.out &&
	test_grep -E "^full split/app [0-9a-f]{64}$" split-full.out &&
	base_id=$(cut -d" " -f3 split-full.out) &&
	object=$images/_objects/$base_id &&
	test_path_is_file "$object/image.yaml" &&
	test_path_is_missing "$object/image.sqsh" &&
	test_path_is_file "$images/meta.yaml" &&
	test_grep -E "^[[:space:]]*- id: $base_id$" "$images/meta.yaml" &&
	(
		cd "$split" &&
		"$tiler_bin" images show "split/app:$base_id" 2>"$HOME/split-show.err"
	) >split-show.out &&
	test_grep -E "^  format +squashfs/" split-show.out &&
	test_grep -E "^  payload +blocks$" split-show.out &&
	workflow_block_manifest "$object/image.yaml" >full-blocks &&
	test_line_count -gt 1 full-blocks
'

verify_split_payload () {
	object=$1 &&
	tiler_block_limit=${2:-1048576} &&
	workflow_block_manifest "$object/image.yaml" "${3:-2}" >full-blocks &&
	awk "/^    size:/ { print \$2 }" "$object/image.yaml" >full-size &&
	awk "/^    sha256:/ { print \$2 }" "$object/image.yaml" >full-digest &&
	test_line_count = 1 full-size &&
	test_line_count = 1 full-digest &&
	test_grep -E "^[0-9]+$" full-size &&
	test_grep -E "^[0-9a-f]{64}$" full-digest &&
	block_count=$(wc -l <full-blocks) &&
	block_index=0 &&
	: >full-reassembled &&
	: >full-names &&
	while read -r tiler_block tiler_size tiler_sum
	do
		block_index=$((block_index + 1)) &&
		test_path_is_file "$object/$tiler_block" &&
		actual_size=$(wc -c <"$object/$tiler_block") &&
		test "$actual_size" = "$tiler_size" &&
		test "$actual_size" -gt 0 &&
		test "$actual_size" -le "$tiler_block_limit" &&
		if test "$block_index" -lt "$block_count"
		then
			test "$actual_size" = "$tiler_block_limit"
		fi &&
		sha256sum "$object/$tiler_block" >block-sum &&
		actual_sum=$(cut -d" " -f1 block-sum) &&
		test "$actual_sum" = "$tiler_sum" &&
		printf "%s\n" "$tiler_block" >>full-names &&
		cat "$object/$tiler_block" >>full-reassembled || return 1
	done <full-blocks &&
	(
		cd "$object" &&
		find . -name "blk.*" -printf "%f\n"
	) >actual-names &&
	LC_ALL=C sort actual-names >actual-names.sorted &&
	test_cmp full-names actual-names.sorted &&
	wc -c <full-reassembled >reassembled-size &&
	test_cmp full-size reassembled-size &&
	sha256sum full-reassembled >reassembled-sum &&
	cut -d" " -f1 reassembled-sum >reassembled-digest &&
	test_cmp full-digest reassembled-digest
}

test_expect_success TILER,LINUX,SQUASHFS_PACK,WORKFLOW_SQUASHFS_CLIENT 'full blocks match their sizes digests and complete logical payload' '
	verify_split_payload "$images/_objects/$base_id" &&
	base_size=$(cat full-size)
'

# The mounted lifecycle consumes the full fixture shared by the packing checks.
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'mount the split full and publish a configured split increment' '
	test_atexit cleanup_split &&
	reassembly_tools=$HOME/reassembly-tools &&
	mkdir -p "$reassembly_tools" &&
	for tiler_tool in git mksquashfs squashfuse_ll fuse-overlayfs fusermount3
	do
		tiler_tool_path=$(command -v "$tiler_tool") &&
		ln -s "$tiler_tool_path" "$reassembly_tools/$tiler_tool" || return 1
	done &&
	(
		cd "$split" &&
		"$tiler_bin" session new split-edit 2>"$HOME/split-new.err" &&
		"$tiler_bin" mount -s split-edit "split/app:$base_id" "$edit" 2>"$HOME/split-mount.err"
	) >split-mount.out &&
	test_grep -E "^mounted " split-mount.out &&
	split_mounts >live-mounts &&
	test_grep -F -x "$edit" live-mounts &&
	for tiler_binary in base-a base-b
	do
		test_cmp "$prepare/bin/$tiler_binary.bin" "$edit/bin/$tiler_binary.bin" || return 1
	done &&
	dd if=/dev/urandom of=delta-expected.bin bs=1048576 count=2 status=none &&
	cp delta-expected.bin "$edit/bin/delta.bin" &&
	printf "note published in the increment\n" >published-note &&
	cp published-note "$edit/note.txt" &&
	rm "$edit/obsolete.txt" &&
	printf "delete after publication\n" >"$edit/published-delete.txt" &&
	test_cmp delta-expected.bin "$sessions/split-edit/runtime/m1/upper/bin/delta.bin" &&
	(
		cd "$edit" &&
		"$tiler_bin" build -i --format squashfs 2>"$HOME/split-incr.err"
	) >split-incr.out &&
	test_grep -E "^incr split/app [0-9a-f]{64}$" split-incr.out &&
	delta_id=$(cut -d" " -f3 split-incr.out) &&
	test "$delta_id" != "$base_id" &&
	test_path_is_missing "$images/_objects/$delta_id/image.sqsh" &&
	verify_split_payload "$images/_objects/$delta_id" &&
	(
		cd "$split" &&
		"$tiler_bin" images show "split/app:$delta_id" 2>"$HOME/split-incr-show.err"
	) >split-incr-show.out &&
	test_grep -E "^  parent +$base_id$" split-incr-show.out &&
	test_grep -E "^  payload +blocks$" split-incr-show.out &&
	printf "unpublished later note with distinct contents\n" >pending-note &&
	cp pending-note "$edit/note.txt" &&
	printf "unpublished addition\n" >pending-file &&
	cp pending-file "$edit/pending.txt" &&
	rm "$edit/published-delete.txt"
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'a fresh session reads the split full and increment without the original upper' '
	(
		cd "$split" &&
		"$tiler_bin" session close split-edit 2>"$HOME/split-close.err"
	) >split-close.out &&
	find "$edit" -mindepth 1 >split-edit-entries &&
	test_must_be_empty split-edit-entries &&
	test_cmp delta-expected.bin "$sessions/split-edit/runtime/m1/upper/bin/delta.bin" &&
	rm -rf "$cache" &&
	(
		cd "$split" &&
		env PATH="$reassembly_tools" "$tiler_bin" mount --verify --rm "split/app:$delta_id" "$verify" 2>"$HOME/split-verify.err"
	) >split-verify.out &&
	verify_session=$(sed -n "s/^new session created: //p" split-verify.out) &&
	test -n "$verify_session" &&
	test "$verify_session" != split-edit &&
	check_split_view "$verify" &&
	for tiler_id in "$base_id" "$delta_id"
	do
		test_path_is_file "$cache/$tiler_id.sqsh" &&
		sha256sum "$cache/$tiler_id.sqsh" >cached.sum &&
		cached_sum=$(cut -d" " -f1 cached.sum) &&
		test "$cached_sum" = "$tiler_id" || return 1
	done &&
	test_path_is_missing "$sessions/$verify_session/runtime/m1/upper/bin/delta.bin" &&
	(
		cd "$split" &&
		"$tiler_bin" umount "$verify" 2>"$HOME/split-verify-umount.err"
	) >split-verify-umount.out &&
	find "$verify" -mindepth 1 >split-verify-entries &&
	test_must_be_empty split-verify-entries &&
	test_path_is_missing "$sessions/$verify_session"
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'replaying split layers prunes published data and preserves later edits and deletions' '
	(
		cd "$split" &&
		"$tiler_bin" session open split-edit 2>"$HOME/split-replay.err"
	) >split-replay.out &&
	test_path_is_missing "$sessions/split-edit/runtime/m1/upper/bin/delta.bin" &&
	test_cmp delta-expected.bin "$edit/bin/delta.bin" &&
	test_cmp pending-note "$edit/note.txt" &&
	test_cmp pending-file "$edit/pending.txt" &&
	test_path_is_missing "$edit/published-delete.txt" &&
	test_path_is_missing "$edit/obsolete.txt" &&
	test_grep ! -F "\"served_tip\":" "$sessions/split-edit/session.json" &&
	(
		cd "$split" &&
		"$tiler_bin" session close split-edit
	) >split-replay-close.out
'

for tiler_damage in missing truncated corrupted
do
	test_expect_success TILER,LINUX,FUSE_TOOLCHAIN "$tiler_damage split blocks fail cleanly and mount again after restoration" '
		tiler_damaged_block=$images/_objects/$delta_id/blk.000 &&
		cp -p "$tiler_damaged_block" "$HOME/block.before" &&
		cp "$images/meta.yaml" damage-meta.before &&
		cp "$sessions/split-edit/session.json" damage-session.before &&
		test_when_finished '\''cp -p "$HOME/block.before" "$tiler_damaged_block"'\'' &&
		case "$tiler_damage" in
		missing)
			rm "$tiler_damaged_block"
			;;
		truncated)
			dd if="$HOME/block.before" of="$tiler_damaged_block" bs=1 count=128 status=none
			;;
		corrupted)
			dd if="$HOME/block.before" of=original-byte bs=1 count=1 status=none &&
			od -An -tu1 original-byte >original-byte.value &&
			read -r tiler_original_byte <original-byte.value &&
			tiler_octal=$(printf "%03o" "$((tiler_original_byte ^ 1))") &&
			printf "%b" "\\$tiler_octal" >replacement-byte &&
			dd if=replacement-byte of="$tiler_damaged_block" bs=1 conv=notrunc status=none &&
			touch -r "$HOME/block.before" "$tiler_damaged_block"
			;;
		esac &&
		rm -rf "$cache" &&
		mkdir "$cache" &&
		(
			cd "$split" &&
			test_expect_code 1 env PATH="$reassembly_tools" "$tiler_bin" mount --verify --rm "split/app:$delta_id" "$verify" >"$HOME/damage.out" 2>"$HOME/damage.err"
		) &&
		test_must_be_empty damage.out &&
		test_grep -F blk.000 damage.err &&
		test_cmp damage-meta.before "$images/meta.yaml" &&
		test_cmp damage-session.before "$sessions/split-edit/session.json" &&
		find "$cache" -type f >damage-cache &&
		test_must_be_empty damage-cache &&
		find "$sessions" -name session.json >damage-records &&
		test_line_count = 1 damage-records &&
		cp -p "$HOME/block.before" "$tiler_damaged_block" &&
		(
			cd "$split" &&
			env PATH="$reassembly_tools" "$tiler_bin" mount --verify --rm "split/app:$delta_id" "$verify" 2>"$HOME/damage-restored.err"
		) >damage-restored.out &&
		check_split_view "$verify" &&
		(
			cd "$split" &&
			"$tiler_bin" umount "$verify"
		) >damage-unmount.out
	'
done

for tiler_shape in full chain linked
do
	test_expect_success TILER,LINUX,FUSE_TOOLCHAIN,SPLIT_CONCATFS "installed concatfs mounts the split $tiler_shape without a materialized image" '
		tiler_library=split/app &&
		if test "$tiler_shape" = full
		then
			tiler_selected_id=$base_id &&
			tiler_concat_count=1
		else
			tiler_selected_id=$delta_id &&
			tiler_concat_count=2
		fi &&
		if test "$tiler_shape" = linked
		then
			tiler_library=split/linked &&
			(
				cd "$split" &&
				"$tiler_bin" build -I "$tiler_library" --link "$images" --hash "$delta_id" 2>"$HOME/concat-link.err"
			) >concat-link.out
		fi &&
		rm -rf "$cache" &&
		mkdir "$cache" &&
		rm -f full-reassembled &&
		(
			cd "$split" &&
			"$tiler_bin" mount --verify --read-only --rm "$tiler_library:$tiler_selected_id" "$verify" 2>"$HOME/concat-mount.err"
		) >concat-mount.out &&
		concat_session=$(sed -n "s/^new session created: //p" concat-mount.out) &&
		test -n "$concat_session" &&
		concat_runtime=$sessions/$concat_session/runtime/m1 &&
		split_mounts >concat-mounts &&
		sed -n "\\|$concat_runtime/concat-[0-9][0-9]*\$|p" concat-mounts >concat-transits &&
		test_line_count = "$tiler_concat_count" concat-transits &&
		sha256sum "$concat_runtime/concat-0/image.sqsh-concat-view" >concat.sum &&
		concat_sum=$(cut -d" " -f1 concat.sum) &&
		test "$concat_sum" = "$tiler_selected_id" &&
		if test "$tiler_shape" != full
		then
			check_split_view "$verify" &&
			sha256sum "$concat_runtime/concat-1/image.sqsh-concat-view" >concat-base.sum &&
			concat_base_sum=$(cut -d" " -f1 concat-base.sum) &&
			test "$concat_base_sum" = "$base_id"
		else
			test_cmp "$prepare/bin/base-a.bin" "$verify/bin/base-a.bin" &&
			test_cmp "$prepare/bin/base-b.bin" "$verify/bin/base-b.bin" &&
			test_path_is_missing "$verify/bin/delta.bin"
		fi &&
		find "$cache" "$concat_runtime" -type f -name "*.sqsh" >concat-materialized &&
		test_must_be_empty concat-materialized &&
		test_path_is_missing full-reassembled &&
		(
			cd "$split" &&
			"$tiler_bin" umount "$verify"
		) >concat-unmount.out &&
		test_path_is_missing "$sessions/$concat_session" &&
		if test "$tiler_shape" = linked
		then
			(
				cd "$split" &&
				"$tiler_bin" images remove "$tiler_library"
			) >concat-link-remove.out
		fi &&
		split_mounts >concat-residue &&
		test_must_be_empty concat-residue
	'
done

for tiler_boundary in exact small override disabled
do
	test_expect_success TILER,LINUX,SQUASHFS_PACK,WORKFLOW_SQUASHFS_CLIENT "split configuration handles the $tiler_boundary command-line boundary" '
		case "$tiler_boundary" in
		exact) tiler_split_bytes=$base_size ;;
		small) tiler_split_bytes=$((base_size + 1048576)) ;;
		override) tiler_split_bytes=524288 ;;
		disabled) tiler_split_bytes=0 ;;
		esac &&
		(
			cd "$prepare" &&
			"$tiler_bin" build -I "split/$tiler_boundary" -f --format squashfs --split "$tiler_split_bytes" 2>"$HOME/boundary-build.err"
		) >boundary-build.out &&
		test_grep -E "^full split/$tiler_boundary [0-9a-f]{64}$" boundary-build.out &&
		boundary_id=$(cut -d" " -f3 boundary-build.out) &&
		boundary_object=$split/.tiler/images/split/$tiler_boundary/_objects/$boundary_id &&
		if test "$tiler_boundary" = disabled
		then
			test_path_is_file "$boundary_object/image.sqsh" &&
			find "$boundary_object" -name "blk.*" >boundary-blocks &&
			test_must_be_empty boundary-blocks
		else
			test_path_is_missing "$boundary_object/image.sqsh" &&
			verify_split_payload "$boundary_object" "$tiler_split_bytes" 1 &&
			if test "$tiler_boundary" = exact || test "$tiler_boundary" = small
			then
				test_line_count = 1 full-blocks &&
				test_path_is_missing "$boundary_object/blk.001" &&
				test_grep -F -x "$base_size" full-size
			fi
		fi &&
		(
			cd "$split" &&
			"$tiler_bin" images remove "split/$tiler_boundary"
		) >boundary-remove.out
	'
done

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'the split lifecycle leaves no mounts and reports both cleanup failures' '
	split_mounts >final-mounts &&
	test_must_be_empty final-mounts &&
	cat >"$HOME/fail-unmount" <<-\EOF &&
	#!/bin/sh
	printf "injected unmount failure\n" >&2
	exit 1
	EOF
	chmod +x "$HOME/fail-unmount" &&
	(
		tiler_bin=$HOME/fail-unmount &&
		fusermount3_bin=$HOME/fail-unmount &&
		test_expect_code 1 cleanup_split >"$HOME/cleanup-probe.out" 2>"$HOME/cleanup-probe.err"
	) &&
	test_grep "^tiler umount failed for " cleanup-probe.err &&
	test_grep "^FUSE cleanup also failed for " cleanup-probe.err &&
	test_grep "^injected unmount failure$" cleanup-probe.err
'

test_done
