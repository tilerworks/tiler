//go:build linux || darwin

package activity

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// appendHelperEnv marks a test binary re-executed as an appender. Its
// value carries the managed root, the object directory, the instance
// label, and the number of events, joined with "|".
const appendHelperEnv = "TILER_ACTIVITY_APPEND_HELPER"

// TestHelperAppendProcess is not a test of its own: a re-executed
// child appends events through the real lock, so the parent can check
// that processes serialize instead of interleaving records.
func TestHelperAppendProcess(t *testing.T) {
	spec := os.Getenv(appendHelperEnv)
	if spec == "" {
		t.Skip("not the append helper process")
	}
	parts := strings.Split(spec, "|")
	if len(parts) != 4 {
		t.Fatalf("helper spec = %q", spec)
	}
	root, dir, label := parts[0], parts[1], parts[2]
	count, err := strconv.Atoi(parts[3])
	if err != nil {
		t.Fatalf("helper spec = %q: %v", spec, err)
	}
	w, err := OpenLog(root, dir, time.Now)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	for seq := 1; seq <= count; seq++ {
		event := Event{
			Event:      EventMount,
			Phase:      PhaseSucceeded,
			Session:    "dev",
			InstanceID: label,
			Seq:        seq,
		}
		if _, err := w.Append(event); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
}

// TestAppendSerializesAcrossProcesses appends from several child
// processes at once and requires every record to stay whole.
func TestAppendSerializesAcrossProcesses(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "sessions", "dev")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("cannot locate the test binary: %v", err)
	}
	const processes, each = 3, 20
	var started []*exec.Cmd
	for index := range processes {
		spec := strings.Join([]string{
			root, dir, "proc-" + strconv.Itoa(index), strconv.Itoa(each),
		}, "|")
		cmd := exec.Command(self, "-test.run", "^TestHelperAppendProcess$")
		cmd.Env = append(os.Environ(), appendHelperEnv+"="+spec)
		if err := cmd.Start(); err != nil {
			t.Fatalf("spawn helper: %v", err)
		}
		started = append(started, cmd)
	}
	for _, cmd := range started {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("helper failed: %v", err)
		}
	}
	lines := readLines(t, filepath.Join(dir, LogName))
	if len(lines) != processes*each {
		t.Fatalf("records = %d, want %d", len(lines), processes*each)
	}
	counts := make(map[string]int, processes)
	for _, line := range lines {
		var rec struct {
			InstanceID string `json:"instance_id"`
			Seq        int    `json:"seq"`
		}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("record %q is not JSON: %v", line, err)
		}
		if rec.InstanceID == "" || rec.Seq < 1 || rec.Seq > each {
			t.Fatalf("record %q is not a whole helper event", line)
		}
		counts[rec.InstanceID]++
	}
	if len(counts) != processes {
		t.Fatalf("appending processes = %d, want %d", len(counts), processes)
	}
	for label, count := range counts {
		if count != each {
			t.Fatalf("%s appended %d events, want %d", label, count, each)
		}
	}
}
