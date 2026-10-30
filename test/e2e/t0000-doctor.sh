#!/bin/sh
#
# tiler end-to-end test: `tiler doctor` smoke check.
#
test_description='tiler doctor exits cleanly'

. "$(dirname "$0")/lib-tiler.sh"

# tiler_bin, TILER, LINUX, and FUSE_TOOLCHAIN come from sharness.d/tiler.sh.
# The single assertion: with a tiler binary and the FUSE toolchain
# present, `tiler doctor` exits 0. If either prereq is missing the
# framework reports "ok # skip ... (missing ...)" and the case still
# exits 0.
test_expect_success TILER,LINUX,FUSE_TOOLCHAIN 'tiler doctor exits 0' '
	"$tiler_bin" doctor
'

test_done
