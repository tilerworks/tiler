#!/bin/sh

test_description='Linux split workflow verifies configured block payloads'

. "$(dirname "$0")/lib-tiler.sh"
. "$TEST_DIRECTORY/lib-workflow.sh"

split=$HOME/ws-split
prepare=$split/prepare
images=$split/.tiler/images/split/app

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

test_expect_success TILER,LINUX,SQUASHFS_PACK,WORKFLOW_SQUASHFS_CLIENT 'full blocks match their sizes digests and complete logical payload' '
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
'

test_done
