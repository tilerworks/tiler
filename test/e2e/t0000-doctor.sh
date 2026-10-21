#!/bin/sh
#
# tiler end-to-end test: `tiler doctor` smoke check.
#
# This is the only phase-1 e2e case. It proves the vendored
# git-test-lib framework runs inside the tiler repository by driving
# a single, mount-free command: `tiler doctor` must exit 0.
#
# Scope guard: this case asserts ONLY `tiler doctor`. It does not
# assert `tiler version`, and it does not touch build, mount,
# session, or git clone --reference scenarios; those belong to later
# phases built on this skeleton.

test_description='tiler doctor exits cleanly'

# Resolve paths against this script's location BEFORE sourcing the
# framework. test-lib.sh moves the working directory into a per-run
# "trash directory" and repoints HOME there, so any relative path
# captured afterwards would break.
tiler_case_dir=$(cd "$(dirname "$0")" && pwd)

# The vendored framework lives in a sibling directory. Presetting
# TEST_LIB_DIRECTORY lets test-lib.sh locate itself under both bash
# (which would otherwise use BASH_SOURCE) and dash (which only probes
# paths relative to the caller's cwd and would fail in this layout).
TEST_LIB_DIRECTORY=$(cd "$tiler_case_dir/../git-test-lib" && pwd)
export TEST_LIB_DIRECTORY

# Pin the framework's working directories to this case's directory so
# run artifacts (test-results/, "trash directory.*") always land under
# test/e2e/ -- which .gitignore covers -- no matter which cwd the caller
# invokes the case from (e.g. `make e2e` at the repo root). Without this,
# test-lib.sh defaults TEST_DIRECTORY to $(pwd) and dirties the caller.
# Safe here: TEST_TARGET_DIRECTORY then derives to <repo>/test, which
# differs from TEST_DIRECTORY (<repo>/test/e2e), so the framework's
# "identical directories" guard is not tripped.
TEST_DIRECTORY=$tiler_case_dir
export TEST_DIRECTORY

. "$TEST_LIB_DIRECTORY/test-lib.sh"

# tiler_bin, TILER, and FUSE_TOOLCHAIN come from sharness.d/tiler.sh.
# The single assertion: with a tiler binary and the FUSE toolchain
# present, `tiler doctor` exits 0. If either prereq is missing the
# framework reports "ok # skip ... (missing ...)" and the case still
# exits 0.
test_expect_success TILER,FUSE_TOOLCHAIN 'tiler doctor exits 0' '
	"$tiler_bin" doctor
'

test_done
