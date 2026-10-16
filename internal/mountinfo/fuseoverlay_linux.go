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
func enrichOverlays(mounts []Mount, proc string) {
	for i := range mounts {
		m := &mounts[i]
		if m.FSType != fuseOverlayType || m.Upper != "" || m.Lowers != nil {
			continue
		}
		if upper, lowers, ok := daemonOverlayDirs(proc, m.Point); ok {
			m.Upper, m.Lowers = upper, lowers
		}
	}
}

// daemonOverlayDirs extracts the overlay directories from the
// fuse-overlayfs daemon serving point. The highest pid wins, which
// picks the newest mount when a lazily unmounted daemon briefly
// shares the point with a fresh one.
func daemonOverlayDirs(proc, point string) (string, []string, bool) {
	procs, err := os.ReadDir(proc)
	if err != nil {
		return "", nil, false
	}
	best := -1
	options := ""
	for _, dir := range procs {
		pid, convErr := strconv.Atoi(dir.Name())
		if convErr != nil || pid <= best {
			continue
		}
		opts, ok := overlayDaemonOptions(readCmdline(proc, pid), point)
		if !ok {
			continue
		}
		best, options = pid, opts
	}
	if best < 0 {
		return "", nil, false
	}
	upper, lowers := dirOptions(options)
	if upper == "" && len(lowers) == 0 {
		return "", nil, false
	}
	return upper, lowers, true
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
