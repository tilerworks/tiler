#!/bin/sh
#
# tiler end-to-end test: macOS doctor probes hdiutil, not FUSE.
#
# On Darwin, doctor must exit 0 when hdiutil_bin can run, and the
# FUSE probes that fail a Linux host (fuse_device, fuse_overlayfs,
# fusermount3, squashfuse_subdir) must not be failures. A host whose
# shadow cannot attach an APFS image still passes, and the toolchain
# detail names HFS+ for that reason.
#
# The MACOS and HDIUTIL prerequisites skip these tests on hosts without
# the macOS disk-image toolchain.

test_description='macOS doctor passes on hdiutil and ignores missing FUSE tools'

. "$(dirname "$0")/lib-tiler.sh"

# tiler_bin, hdiutil_bin, TILER, and MACOS come from sharness.d/tiler.sh.

# Keep each probe assertion separate so TAP identifies the failing probe.
# Doctor emits each name immediately before its status in the JSON report.
test_expect_success TILER,MACOS,HDIUTIL 'doctor exits 0' '
	"$tiler_bin" doctor --json >doctor.json 2>doctor.err
'

test_expect_success TILER,MACOS,HDIUTIL 'the image toolchain passes' '
	sed -n "/\"name\": \"image_toolchain\"/{n;p;}" doctor.json >toolchain-status &&
	test_line_count = 1 toolchain-status &&
	test_grep -F "\"status\": \"pass\"" toolchain-status
'

for probe in fuse_device fuse_overlayfs fusermount3 squashfuse_subdir
do
	test_expect_success TILER,MACOS,HDIUTIL "$probe does not fail on macOS" '
		sed -n "/\"name\": \"$probe\"/{n;p;}" doctor.json >probe-status &&
		# An omitted Linux-only probe is valid on macOS.
		test_grep ! -F "\"status\": \"fail\"" probe-status
	'
done

test_expect_success TILER,MACOS,HDIUTIL 'the disk-image filesystem detail describes the host' '
	if grep -F "HFS+" doctor.json
	then
		test_grep -F "filesystem HFS+ because attach -shadow cannot use an APFS image" doctor.json
	else
		test_grep -F "\"detail\": \"dmg\"" doctor.json
	fi
'

test_done
