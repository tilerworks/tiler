#!/bin/sh
#
# tiler end-to-end test: TILER_CEILING_DIR bounds the upward config walk.
#
# tiler discovers project-level .tiler.yaml files by walking upward from
# the working directory. In a test sandbox that walk must not escape into
# the surrounding tree. TILER_CEILING_DIR names a directory that stops
# the walk: the ceiling directory's own config still applies, but a
# config higher up is excluded.
#
# Layout built under the per-run trash HOME:
#
#   $HOME/zone/.tiler.yaml          cache_dir: marker-cache  (above ceiling)
#   $HOME/zone/sandbox/deep/        working directory             (ceiling = sandbox)
#
# With the default sandbox ceiling the walk reaches zone; with
# TILER_CEILING_DIR=$HOME/zone/sandbox it stops below zone, so the marker
# is excluded and the auto-created $HOME/.tiler.yaml default stands. No
# FUSE toolchain is needed: the assertion reads `doctor --json`.

test_description='TILER_CEILING_DIR bounds the upward config walk'

. "$(dirname "$0")/lib-tiler.sh"

# tiler_bin and TILER come from sharness.d/tiler.sh.
# The bootstrap ceiling is the physical trash root, including with --root
# pointing through a symlink. The narrower ceiling below overrides it.

test_expect_success TILER 'lay out a sandbox with a marker config above the ceiling' '
	sandbox_root=$(pwd -P) &&
	mkdir -p "$HOME/zone/sandbox/deep" &&
	printf "cache_dir: marker-cache\n" >"$HOME/zone/.tiler.yaml"
'

# doctor exits 1 when a FUSE probe fails on an unequipped host, but it
# still writes the full --json document (config first). Capture stdout
# and assert on the resolved cache_dir regardless of the verdict.
test_expect_success TILER 'the sandbox ceiling includes the marker inside the sandbox' '
	(
		cd -P "$HOME/zone/sandbox/deep" &&
		status=0 &&
		{ "$tiler_bin" doctor --json >"$HOME/sandbox.json" 2>"$HOME/sandbox.err" || status=$?; } &&
		{ test "$status" -eq 0 || test "$status" -eq 1; }
	) &&
	test_grep -F "\"cache_dir\": \"$sandbox_root/zone/marker-cache\"" "$HOME/sandbox.json"
'

test_expect_success TILER 'TILER_CEILING_DIR stops the walk below the marker' '
	(
		cd -P "$HOME/zone/sandbox/deep" &&
		ceiling=$(cd -P "$HOME/zone/sandbox" && pwd -P) &&
		status=0 &&
		{ TILER_CEILING_DIR="$ceiling" "$tiler_bin" doctor --json >"$HOME/ceiling.json" 2>"$HOME/ceiling.err" || status=$?; } &&
		{ test "$status" -eq 0 || test "$status" -eq 1; }
	) &&
	test_grep -F "\"cache_dir\": \"$HOME/.tiler/cache\"" "$HOME/ceiling.json" &&
	test_grep ! -F "marker-cache" "$HOME/ceiling.json"
'

test_done
