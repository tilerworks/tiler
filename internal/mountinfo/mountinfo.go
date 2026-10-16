// Package mountinfo holds the mount table data model and the pure
// build-mode decision. Parsing and deciding never touch the host:
// Linux-only loading lives in the build-tagged file, and every other
// platform works on injected tables.
package mountinfo

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// Mount is one mount table entry reduced to the fields build
// decisions need.
type Mount struct {
	// Point is the cleaned mount point path.
	Point string
	// FSType is the filesystem type, for example fuse or overlay.
	FSType string
	// Source is the mount source. Internal helpers mount image
	// lowers with the real library path here.
	Source string
	// SuperOpts is the raw super block option string.
	SuperOpts string
	// Upper is the upper directory of a writable merged view when
	// the options expose one. Read-only views leave it empty.
	Upper string
	// Lowers lists the lower directories of a merged view when the
	// options expose them.
	Lowers []string
}

// minMountFields is the smallest legal entry: id, parent, maj:min,
// root, point, options, separator, fstype, source, super options.
const minMountFields = 10

// Parse reads mountinfo formatted lines. A malformed line reports
// its line number and aborts the whole table.
func Parse(r io.Reader) ([]Mount, error) {
	scan := bufio.NewScanner(r)
	var mounts []Mount
	line := 0
	for scan.Scan() {
		line++
		text := strings.TrimSpace(scan.Text())
		if text == "" {
			continue
		}
		mount, err := parseLine(text)
		if err != nil {
			return nil, fmt.Errorf("mountinfo: line %d: %w", line, err)
		}
		mounts = append(mounts, mount)
	}
	if err := scan.Err(); err != nil {
		return nil, fmt.Errorf("mountinfo: %w", err)
	}
	return mounts, nil
}

func parseLine(line string) (Mount, error) {
	fields := strings.Fields(line)
	sep := -1
	for i, field := range fields {
		if field == "-" {
			sep = i
			break
		}
	}
	if sep < 0 {
		return Mount{}, errors.New("missing filesystem separator")
	}
	if sep < 6 || len(fields) < sep+4 || len(fields) < minMountFields {
		return Mount{}, errors.New("truncated mount entry")
	}
	mount := Mount{
		Point:     filepath.Clean(unescape(fields[4])),
		FSType:    unescape(fields[sep+1]),
		Source:    unescape(fields[sep+2]),
		SuperOpts: fields[sep+3],
	}
	mount.Upper, mount.Lowers = dirOptions(fields[5], mount.SuperOpts)
	return mount, nil
}

// dirOptions extracts upperdir and lowerdir from the per-mount and
// super option strings. Values keep their first occurrence.
func dirOptions(optionSets ...string) (string, []string) {
	var upper string
	var lowers []string
	for _, options := range optionSets {
		for _, token := range strings.Split(options, ",") {
			if value, ok := strings.CutPrefix(token, "upperdir="); ok && upper == "" {
				upper = filepath.Clean(unescape(value))
				continue
			}
			if value, ok := strings.CutPrefix(token, "lowerdir="); ok && lowers == nil {
				for _, dir := range strings.Split(value, ":") {
					if dir != "" {
						lowers = append(lowers, filepath.Clean(unescape(dir)))
					}
				}
			}
		}
	}
	return upper, lowers
}

// unescape decodes the octal sequences the kernel uses for space,
// tab, newline, and backslash inside mountinfo fields.
func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	s = strings.ReplaceAll(s, `\040`, " ")
	s = strings.ReplaceAll(s, `\011`, "\t")
	s = strings.ReplaceAll(s, `\012`, "\n")
	return strings.ReplaceAll(s, `\134`, `\`)
}

// isViewMount reports a filesystem that shows image content: any
// FUSE mount or an overlay merged view.
func isViewMount(mount Mount) bool {
	return mount.FSType == "overlay" || mount.FSType == "fuse" ||
		strings.HasPrefix(mount.FSType, "fuse.")
}

// findMount returns the top-most mount at an exact point. Later
// table lines shadow earlier mounts of the same path.
func findMount(table []Mount, point string) (Mount, bool) {
	found := Mount{}
	ok := false
	for _, mount := range table {
		if mount.Point == point {
			found, ok = mount, true
		}
	}
	return found, ok
}
