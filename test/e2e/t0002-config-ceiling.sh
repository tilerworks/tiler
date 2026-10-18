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
#   $HOME/zone/.tiler.yaml          runtime_dir: marker-runtime  (above ceiling)
#   $HOME/zone/sandbox/deep/        working directory             (ceiling = sandbox)
#
# Without the ceiling the walk reaches zone and applies the marker; with
# TILER_CEILING_DIR=$HOME/zone/sandbox it stops below zone, so the marker
# is excluded and the auto-created $HOME/.tiler.yaml default stands. No
# FUSE toolchain is needed: the assertion reads `doctor --json`.

test_description='TILER_CEILING_DIR bounds the upward config walk'

tiler_case_dir=$(cd "$(dirname "$0")" && pwd)
TEST_LIB_DIRECTORY=$(cd "$tiler_case_dir/../git-test-lib" && pwd)
export TEST_LIB_DIRECTORY
TEST_DIRECTORY=$tiler_case_dir
export TEST_DIRECTORY

. "$TEST_LIB_DIRECTORY/test-lib.sh"

tiler_repo_root=$(cd "$tiler_case_dir/../.." && pwd)
tiler_bin=$tiler_repo_root/bin/tiler
if ! test -x "$tiler_bin"
then
	tiler_bin=$(command -v tiler) || tiler_bin=
fi
if test -n "$tiler_bin"
then
	test_set_prereq TILER
fi

test_expect_success TILER 'lay out a sandbox with a marker config above the ceiling' '
	mkdir -p "$HOME/zone/sandbox/deep" &&
	printf "runtime_dir: marker-runtime\n" >"$HOME/zone/.tiler.yaml"
'

# doctor exits 1 when a FUSE probe fails on an unequipped host, but it
# still writes the full --json document (config first). Capture stdout
# and assert on the resolved runtime_dir regardless of the verdict.
test_expect_success TILER 'without a ceiling the walk applies the marker above' '
	cd "$HOME/zone/sandbox/deep" &&
	{ "$tiler_bin" doctor --json >"$HOME/no-ceiling.json" 2>"$HOME/no-ceiling.err" || true; } &&
	grep -qF "\"runtime_dir\": \"$HOME/zone/marker-runtime\"" "$HOME/no-ceiling.json"
'

test_expect_success TILER 'TILER_CEILING_DIR stops the walk below the marker' '
	cd "$HOME/zone/sandbox/deep" &&
	{ TILER_CEILING_DIR="$HOME/zone/sandbox" "$tiler_bin" doctor --json >"$HOME/ceiling.json" 2>"$HOME/ceiling.err" || true; } &&
	grep -qF "\"runtime_dir\": \"$HOME/.tiler/runtime\"" "$HOME/ceiling.json" &&
	! grep -qF "marker-runtime" "$HOME/ceiling.json"
'

test_done
