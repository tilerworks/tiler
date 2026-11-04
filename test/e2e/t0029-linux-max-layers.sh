#!/bin/sh

test_description='Linux image builds and mounts honor the configured chain limit'

. "$(dirname "$0")/lib-tiler.sh"

src=$HOME/src
view=$HOME/work/view
verify=$HOME/work/verify
meta=$HOME/.tiler/images/layers/app/meta.yaml

test_atexit '
	for tiler_point in "$view" "$verify"
	do
		"$tiler_bin" umount "$tiler_point" >/dev/null 2>&1 ||
		"$fusermount3_bin" -uz "$tiler_point" >/dev/null 2>&1 || true
	done
'

# Later cases extend this mounted fixture; every case needs the Linux
# FUSE toolchain and the atexit cleanup owns both views throughout.
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'two-layer limit rejects a new increment without changing metadata or upper' '
	cat >"$HOME/.tiler.yaml" <<-\EOF &&
	version: 1
	max_layers: 2
	EOF
	mkdir -p "$src" "$view" "$verify" &&
	echo base >"$src/base" &&
	"$tiler_bin" build -s 0 -I layers/app "$src" >base.out 2>base.err &&
	"$tiler_bin" mount --rm layers/app "$view" >mount.out 2>mount.err &&
	echo second >"$view/second" &&
	(cd "$view" && "$tiler_bin" build -s 0 -i 2>"$HOME/second.err") >second.out &&
	"$tiler_bin" umount "$view" >umount.out 2>umount.err &&
	"$tiler_bin" mount --rm layers/app "$view" >mount-second.out 2>mount-second.err &&
	echo third >"$HOME/expected-third" &&
	cp "$HOME/expected-third" "$view/third" &&
	cp "$meta" before-meta &&
	(
		cd "$view" &&
		test_expect_code 1 "$tiler_bin" build -s 0 -i 2>"$HOME/rejected-build.err"
	) >rejected-build.out &&
	test_must_be_empty rejected-build.out &&
	test_grep "max_layers is 2" rejected-build.err &&
	test_cmp before-meta "$meta" &&
	test_cmp "$HOME/expected-third" "$view/third"
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'raising the limit allows publication while lowering it rejects a fresh mount' '
	cat >"$HOME/.tiler.yaml" <<-\EOF &&
	version: 1
	max_layers: 3
	EOF
	(cd "$view" && "$tiler_bin" build -s 0 -i 2>"$HOME/third.err") >third.out &&
	cat >"$HOME/.tiler.yaml" <<-\EOF &&
	version: 1
	max_layers: 2
	EOF
	test_expect_code 1 "$tiler_bin" mount --rm layers/app "$verify" >rejected-mount.out 2>rejected-mount.err &&
	test_must_be_empty rejected-mount.out &&
	test_grep "the limit is 2" rejected-mount.err &&
	"$tiler_bin" images list -v >list.out 2>list.err &&
	test_grep "layers/app" list.out &&
	cat >"$HOME/.tiler.yaml" <<-\EOF &&
	version: 1
	max_layers: 3
	EOF
	"$tiler_bin" mount --rm layers/app "$verify" >verify.out 2>verify.err &&
	test_cmp "$src/base" "$verify/base" &&
	test_cmp "$HOME/expected-third" "$verify/third" &&
	"$tiler_bin" umount "$verify" >verify-umount.out 2>verify-umount.err &&
	"$tiler_bin" umount "$view" >view-umount.out 2>view-umount.err
'

test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'commented default permits building and mounting beyond eight layers' '
	cat >"$HOME/.tiler.yaml" <<-\EOF &&
	version: 1
	# max_layers: 32
	EOF
	"$tiler_bin" mount --rm layers/app "$view" >grow-mount.out 2>grow-mount.err &&
	for tiler_layer in 4 5 6 7 8 9
	do
		echo "$tiler_layer" >"$HOME/expected-layer-$tiler_layer" &&
		cp "$HOME/expected-layer-$tiler_layer" "$view/layer-$tiler_layer" &&
		(cd "$view" && "$tiler_bin" build -s 0 -i 2>"$HOME/layer-$tiler_layer.err") >"layer-$tiler_layer.out" &&
		"$tiler_bin" umount "$view" >grow-umount.out 2>grow-umount.err &&
		"$tiler_bin" mount --rm layers/app "$view" >grow-mount.out 2>grow-mount.err || return 1
	done &&
	test_cmp "$src/base" "$view/base" &&
	test_cmp "$HOME/expected-third" "$view/third" &&
	for tiler_layer in 4 5 6 7 8 9
	do
		test_cmp "$HOME/expected-layer-$tiler_layer" "$view/layer-$tiler_layer" || return 1
	done &&
	grep "^[[:space:]]*- id:" "$meta" >versions &&
	test_line_count = 9 versions
'

test_done
