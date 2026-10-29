# Shared e2e helpers for tiler. Loaded from test/e2e/sharness.d/*.sh
# after test-lib.sh defines test_set_prereq and test_lazy_prereq.
#
# Provides:
#   tiler_bin          repository bin/tiler, else a tiler on PATH
#   hdiutil_bin        hdiutil on PATH, the macOS disk-image tool
#   fusermount3_bin    fusermount3 on PATH, the FUSE unmount helper
#   TILER              set when tiler_bin is non-empty
#   LINUX              uname -s is Linux (current mount stack)
#   MACOS              uname -s is Darwin (future macOS mount stack)
#   FUSE_TOOLCHAIN     Linux + /dev/fuse + squashfs/overlay tools (+ git)
#   EROFS_TOOLCHAIN    Linux + /dev/fuse + erofs/overlay tools (+ git)
#   KILL_VIEW          Linux pgrep + /proc for kill_view
#   kill_view <mp>     SIGKILL FUSE daemons serving one mountpoint

# Prefer the repository build; fall back to a tiler found on PATH.
# TEST_DIRECTORY is pinned to test/e2e/ by each case before sourcing.
tiler_repo_root=$(cd "$TEST_DIRECTORY/../.." && pwd)
tiler_bin=$tiler_repo_root/bin/tiler
if ! test -x "$tiler_bin"
then
	tiler_bin=$(command -v tiler) || tiler_bin=
fi
if test -n "$tiler_bin"
then
	test_set_prereq TILER
fi

# hdiutil_bin is the hdiutil program name. tiler resolves it on
# PATH. Cases call this variable instead of spelling a path.
hdiutil_bin=hdiutil

# fusermount3_bin is the program name of the FUSE unmount helper.
# Cases call it when tiler umount cannot detach a view. PATH resolves
# the name at the call.
fusermount3_bin=fusermount3

# OS gates for the current Linux mount stack and the coming macOS
# image-format mounts. Cases that need a real mount list LINUX (or
# later MACOS) so make e2e skips the wrong host instead of failing.
case "$(uname -s)" in
Linux)
	test_set_prereq LINUX
	;;
Darwin)
	test_set_prereq MACOS
	;;
esac

# A real squashfs mount needs Linux, the FUSE device, and the
# squashfs/overlay toolchain (the same set `tiler doctor` reports as
# fail-level). git is included because doctor always probes it. Lazy:
# only evaluated when a test_expect_success lists FUSE_TOOLCHAIN.
test_lazy_prereq FUSE_TOOLCHAIN '
	test_have_prereq LINUX &&
	test -e /dev/fuse &&
	command -v squashfuse_ll >/dev/null 2>&1 &&
	command -v fuse-overlayfs >/dev/null 2>&1 &&
	command -v "$fusermount3_bin" >/dev/null 2>&1 &&
	command -v mksquashfs >/dev/null 2>&1 &&
	command -v git >/dev/null 2>&1
'

# erofs packing and mounting need Linux plus the erofs stack and the
# shared FUSE tools. A squashfs-only or non-Linux host leaves this
# prereq unmet and the case skips.
test_lazy_prereq EROFS_TOOLCHAIN '
	test_have_prereq LINUX &&
	test -e /dev/fuse &&
	command -v mkfs.erofs >/dev/null 2>&1 &&
	command -v erofsfuse >/dev/null 2>&1 &&
	command -v fuse-overlayfs >/dev/null 2>&1 &&
	command -v "$fusermount3_bin" >/dev/null 2>&1 &&
	command -v git >/dev/null 2>&1
'

# Packing a squashfs image needs mksquashfs and does not need a
# mount. GC cases that publish a full image and never mount list
# this prereq so a host without /dev/fuse still runs them.
test_lazy_prereq SQUASHFS_PACK '
	command -v mksquashfs >/dev/null 2>&1
'

# kill_view needs pgrep and /proc/<pid>/cmdline. Gate crash/stale
# assertions on this alone so mount cases without a kill still run
# when pgrep is missing; require LINUX because /proc is Linux-only.
test_lazy_prereq KILL_VIEW '
	test_have_prereq LINUX &&
	command -v pgrep >/dev/null 2>&1 &&
	test -d /proc
'

# kill_view SIGKILLs the FUSE daemon(s) serving one mountpoint, so a
# recovery or status case can simulate a daemon death while the mount
# table entry stays behind. It matches the mountpoint against the full
# command line (pgrep -f), skips this shell, and re-confirms each
# candidate against /proc/<pid>/cmdline with a fixed-string match, so a
# regex over-match or a path with spaces never kills the wrong process.
# Returns success only when it actually killed a daemon.
kill_view() {
	_mp=${1:-}
	if test -z "$_mp"
	then
		echo "usage: kill_view mountpoint" >&2
		return 2
	fi
	_found=0
	for _pid in $(pgrep -f "$_mp" 2>/dev/null)
	do
		test "$_pid" = "$$" && continue
		tr "\0" " " <"/proc/$_pid/cmdline" 2>/dev/null |
			grep -qF "$_mp" || continue
		kill -9 "$_pid" 2>/dev/null && _found=1
	done
	test "$_found" -eq 1
}
