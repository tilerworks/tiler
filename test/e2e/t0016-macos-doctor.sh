#!/bin/sh
#
# tiler end-to-end test: macOS doctor probes hdiutil, not FUSE.
#
# On Darwin, doctor must exit 0 when /usr/bin/hdiutil can run, and the
# FUSE probes that fail a Linux host (fuse_device, fuse_overlayfs,
# fusermount3, squashfuse_subdir) must not be failures. A host whose
# shadow cannot attach an APFS image still passes, and the toolchain
# detail names HFS+ for that reason.
#
# The case lists only TILER,MACOS. Linux make e2e skips it. A Darwin
# host without /usr/bin/hdiutil skips the file instead of failing.

test_description='macOS doctor passes on hdiutil and ignores missing FUSE tools'

tiler_case_dir=$(cd "$(dirname "$0")" && pwd)
TEST_LIB_DIRECTORY=$(cd "$tiler_case_dir/../git-test-lib" && pwd)
export TEST_LIB_DIRECTORY
TEST_DIRECTORY=$tiler_case_dir
export TEST_DIRECTORY

. "$TEST_LIB_DIRECTORY/test-lib.sh"

# tiler_bin, TILER, and MACOS come from sharness.d/tiler.sh.

if test "$(uname -s)" = Darwin && ! test -x /usr/bin/hdiutil
then
	skip_all='skipping macOS doctor; /usr/bin/hdiutil is missing'
	test_done
fi

# JSON rows are name then status. A missing name is not a failure.
# An HFS+ detail is the supported host difference; otherwise the
# toolchain detail is the plain dmg pass.
test_expect_success TILER,MACOS 'doctor exits 0 and does not fail on fuse tools' '
	"$tiler_bin" doctor --json >doctor.json 2>doctor.err &&
	grep -A1 "\"name\": \"image_toolchain\"" doctor.json |
		grep -q "\"status\": \"pass\"" &&
	! grep -A1 "\"name\": \"fuse_device\"" doctor.json |
		grep -q "\"status\": \"fail\"" &&
	! grep -A1 "\"name\": \"fuse_overlayfs\"" doctor.json |
		grep -q "\"status\": \"fail\"" &&
	! grep -A1 "\"name\": \"fusermount3\"" doctor.json |
		grep -q "\"status\": \"fail\"" &&
	! grep -A1 "\"name\": \"squashfuse_subdir\"" doctor.json |
		grep -q "\"status\": \"fail\"" &&
	if grep -q "HFS+" doctor.json
	then
		grep -F "filesystem HFS+ because attach -shadow cannot use an APFS image" doctor.json
	else
		grep -F "\"detail\": \"dmg\"" doctor.json
	fi
'

test_done
