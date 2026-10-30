//go:build linux

package mountinfo

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// fuseOverlayType is the filesystem type a fuse-overlayfs merged
// view carries in the kernel mount table.
const fuseOverlayType = "fuse.fuse-overlayfs"

// procRoot is the process tree the overlay enrichment scans.
const procRoot = "/proc"

// overlayDaemon is the newest fuse-overlayfs process serving one
// mount point. The highest pid wins when a lazily unmounted daemon
// briefly shares the point with a fresh one.
type overlayDaemon struct {
	pid    int
	upper  string
	lowers []string
}

// enrichOverlays recovers the overlay layout of fuse-overlayfs
// mounts. The daemon consumes its lowerdir, upperdir, and workdir
// options, so the kernel mount table shows an anonymous entry, and
// every decision that needs the writable upper or the lowers would
// see none. The daemon command line is the only place the options
// survive, so each anonymous entry is matched with the newest daemon
// whose last argument is the mount point, and the regular option
// parser reads the directories back. An entry whose daemon is gone
// stays anonymous on purpose: the mount is stale, and the callers
// already treat a stale entry as teardown work instead of identity.
//
// The process tree is scanned once. Each anonymous mount used to
// reread every command line, so one Load walked /proc once per mount.
func enrichOverlays(mounts []Mount, proc string) {
	if !needsOverlayEnrichment(mounts) {
		return
	}
	byPoint := scanOverlayDaemons(proc)
	for i := range mounts {
		m := &mounts[i]
		if !anonymousOverlay(m) {
			continue
		}
		hit, ok := byPoint[m.Point]
		if !ok {
			continue
		}
		m.Upper, m.Lowers = hit.upper, hit.lowers
	}
}

// needsOverlayEnrichment reports whether any mount still needs a
// daemon lookup. Parsed entries and other filesystem types skip the
// process scan entirely.
func needsOverlayEnrichment(mounts []Mount) bool {
	for i := range mounts {
		if anonymousOverlay(&mounts[i]) {
			return true
		}
	}
	return false
}

// anonymousOverlay is a fuse-overlayfs entry whose directories the
// kernel table did not carry.
func anonymousOverlay(m *Mount) bool {
	return m.FSType == fuseOverlayType && m.Upper == "" && m.Lowers == nil
}

// scanOverlayDaemons indexes fuse-overlayfs daemons by mount point.
// A missing process tree yields an empty index, which leaves every
// anonymous entry unchanged.
func scanOverlayDaemons(proc string) map[string]overlayDaemon {
	found := make(map[string]overlayDaemon)
	procs, err := os.ReadDir(proc)
	if err != nil {
		return found
	}
	for _, dir := range procs {
		pid, convErr := strconv.Atoi(dir.Name())
		if convErr != nil {
			continue
		}
		recordOverlayDaemon(found, proc, pid)
	}
	return found
}

// recordOverlayDaemon keeps the highest-pid daemon for one point.
// The comm file filters out every other process before its command
// line is read. A tree without comm files, which the unit fixtures
// use, falls through to the command line.
func recordOverlayDaemon(found map[string]overlayDaemon, proc string, pid int) {
	if !commMatchesOverlay(proc, pid) {
		return
	}
	argv := readCmdline(proc, pid)
	if len(argv) == 0 {
		return
	}
	point := argv[len(argv)-1]
	opts, ok := overlayDaemonOptions(argv, point)
	if !ok {
		return
	}
	if prev, exists := found[point]; exists && prev.pid >= pid {
		return
	}
	upper, lowers := dirOptions(opts)
	if upper == "" && len(lowers) == 0 {
		return
	}
	found[point] = overlayDaemon{pid: pid, upper: upper, lowers: lowers}
}

// commMatchesOverlay reports whether pid's kernel comm is
// fuse-overlayfs. A missing comm file does not reject the process:
// the command line is the fallback.
func commMatchesOverlay(proc string, pid int) bool {
	data, err := os.ReadFile(filepath.Join(proc, strconv.Itoa(pid), "comm"))
	if err != nil {
		return true
	}
	return strings.TrimRight(string(data), "\n") == "fuse-overlayfs"
}

// overlayDaemonOptions reports whether argv is a fuse-overlayfs
// daemon serving point and joins its -o option sets. The mount point
// is the final argument, exactly as the mount flow passes it.
func overlayDaemonOptions(argv []string, point string) (string, bool) {
	if len(argv) < 3 || filepath.Base(argv[0]) != "fuse-overlayfs" ||
		argv[len(argv)-1] != point {
		return "", false
	}
	var sets []string
	for i := 1; i < len(argv)-1; i++ {
		if argv[i] != "-o" || i+1 >= len(argv)-1 {
			continue
		}
		sets = append(sets, argv[i+1])
		i++
	}
	if len(sets) == 0 {
		return "", false
	}
	return strings.Join(sets, ","), true
}

// readCmdline splits one process command line into arguments. Kernel
// threads and vanished pids read as no arguments.
func readCmdline(proc string, pid int) []string {
	data, err := os.ReadFile(filepath.Join(proc, strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return nil
	}
	line := strings.TrimSuffix(string(data), "\x00")
	if line == "" {
		return nil
	}
	return strings.Split(line, "\x00")
}
