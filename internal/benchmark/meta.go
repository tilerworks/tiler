package benchmark

import (
	"fmt"
	"io"
	"os/exec"
	"strings"
)

func writeMetadata(d Decision, runtimeDir string, w io.Writer) error {
	writable := "no"
	if dirWritable(d.Path) {
		writable = "yes"
	}
	lines := []string{
		fmt.Sprintf("os: %s", goosName()),
		fmt.Sprintf("type: %s", d.Type),
		fmt.Sprintf("path: %s", d.Path),
		fmt.Sprintf("path_writable: %s", writable),
		fmt.Sprintf("runtime_dir: %s", runtimeDir),
		fmt.Sprintf("mode: %s", d.Mode),
		fmt.Sprintf("size: %d", d.Size),
		fmt.Sprintf("file_size: %d", d.FileSize),
	}
	if d.Compress != "" {
		lines = append(lines, fmt.Sprintf("compress: %s", d.Compress))
	}
	probe := "no"
	if d.Probe {
		probe = "yes"
	}
	lines = append(lines, fmt.Sprintf("probe: %s", probe))
	if d.Probe {
		lines = append(lines, fmt.Sprintf("probe_ops: %d", d.ProbeOps))
	}
	lines = append(lines, "", "## device for path")
	for _, line := range lines {
		if _, err := fmt.Fprintln(w, line); err != nil {
			return fmt.Errorf("benchmark: write metadata: %w", err)
		}
	}
	if err := writeDeviceInfo(d.Path, w); err != nil {
		return err
	}
	_, err := fmt.Fprintln(w)
	return err
}

func writeDeviceInfo(path string, w io.Writer) error {
	out, err := exec.Command("df", "-P", path).Output()
	if err != nil {
		return fmt.Errorf("benchmark: df %s: %w", path, err)
	}
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(lines) < 2 {
		return fmt.Errorf("benchmark: df %s returned no rows", path)
	}
	fields := strings.Fields(lines[1])
	if len(fields) < 1 {
		return fmt.Errorf("benchmark: df %s row is empty", path)
	}
	dev := fields[0]
	if _, err := fmt.Fprintf(w, "volume_device: %s\n", dev); err != nil {
		return fmt.Errorf("benchmark: write metadata: %w", err)
	}
	if _, err := fmt.Fprintf(w, "df: %s\n", lines[1]); err != nil {
		return fmt.Errorf("benchmark: write metadata: %w", err)
	}
	return writeDiskutilInfo(dev, w)
}
