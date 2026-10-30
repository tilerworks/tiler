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

# Packing checks stay available without overlay tools or a FUSE device.
test_expect_success TILER,LINUX,SQUASHFS_PACK,WORKFLOW_SQUASHFS_CLIENT 'prepare a project with one MiB blocks and incompressible data' '
	workflow_config "$split" &&
	printf "split_size: 1M\n" >>"$split/.tiler.yaml" &&
	mkdir -p "$prepare/bin" &&
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
	workflow_block_manifest "$object/image.yaml" >full-blocks &&
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
		test "$actual_size" -le 1048576 &&
		if test "$block_index" -lt "$block_count"
		then
			test "$actual_size" = 1048576
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
	verify_split_payload "$images/_objects/$base_id"
'

# The mounted lifecycle consumes the full fixture shared by the packing checks.
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'mount the split full and publish a configured split increment' '
	test_atexit '\''
		(
			cd "$split" || exit
			for tiler_point in "$edit" "$verify"
			do
				"$tiler_bin" umount "$tiler_point" >/dev/null 2>&1 ||
				"$fusermount3_bin" -uz "$tiler_point" >/dev/null 2>&1 || true
			done
		)
	'\'' &&
	(
		cd "$split" &&
		"$tiler_bin" session new split-edit 2>"$HOME/split-new.err" &&
		"$tiler_bin" mount -s split-edit "split/app:$base_id" "$edit" 2>"$HOME/split-mount.err"
	) >split-mount.out &&
	test_grep -E "^mounted " split-mount.out &&
	for tiler_binary in base-a base-b
	do
		test_cmp "$prepare/bin/$tiler_binary.bin" "$edit/bin/$tiler_binary.bin" || return 1
	done &&
	dd if=/dev/urandom of=delta-expected.bin bs=1048576 count=2 status=none &&
	cp delta-expected.bin "$edit/bin/delta.bin" &&
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
	test_grep -E "^  payload +blocks$" split-incr-show.out
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'a fresh session reads the split full and increment without the original upper' '
	(
		cd "$split" &&
		"$tiler_bin" session close split-edit 2>"$HOME/split-close.err"
	) >split-close.out &&
	find "$edit" -mindepth 1 >split-edit-entries &&
	test_must_be_empty split-edit-entries &&
	test_cmp delta-expected.bin "$sessions/split-edit/runtime/m1/upper/bin/delta.bin" &&
	(
		cd "$split" &&
		"$tiler_bin" mount --rm "split/app:$delta_id" "$verify" 2>"$HOME/split-verify.err"
	) >split-verify.out &&
	verify_session=$(sed -n "s/^new session created: //p" split-verify.out) &&
	test -n "$verify_session" &&
	test "$verify_session" != split-edit &&
	for tiler_binary in base-a base-b
	do
		test_cmp "$prepare/bin/$tiler_binary.bin" "$verify/bin/$tiler_binary.bin" || return 1
	done &&
	test_cmp delta-expected.bin "$verify/bin/delta.bin" &&
	test_path_is_missing "$sessions/$verify_session/runtime/m1/upper/bin/delta.bin" &&
	(
		cd "$split" &&
		"$tiler_bin" umount "$verify" 2>"$HOME/split-verify-umount.err"
	) >split-verify-umount.out &&
	find "$verify" -mindepth 1 >split-verify-entries &&
	test_must_be_empty split-verify-entries &&
	test_path_is_missing "$sessions/$verify_session"
'

test_done
