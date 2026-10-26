package benchmark

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestProbeCountsReadsWritesAndRemovesFile(t *testing.T) {
	dir := t.TempDir()
	const ops = 4
	var stdout, stderr bytes.Buffer
	stats, err := Probe(dir, ops, "yes", false, &stdout, &stderr)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if stats.Read.Ops != ops || stats.Write.Ops != ops {
		t.Fatalf("read/write ops = %d/%d, want %d", stats.Read.Ops, stats.Write.Ops, ops)
	}
	if stats.Block != probeBlock || stats.Purge != "yes" {
		t.Fatalf("block/purge = %d %q", stats.Block, stats.Purge)
	}
	if _, statErr := os.Stat(filepath.Join(dir, probeFileName)); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("probe file stat = %v", statErr)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
	assertProbeLines(t, stdout.String(), ops, "yes")
}

func TestProbeLinesGoToStderrWhenTSV(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	stats, err := Probe(dir, 2, "", true, &stdout, &stderr)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if stats.Read.Ops != 2 || stats.Write.Ops != 2 {
		t.Fatalf("read/write ops = %d/%d", stats.Read.Ops, stats.Write.Ops)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q", stdout.String())
	}
	assertProbeLines(t, stderr.String(), 2, "no")
	if _, statErr := os.Stat(filepath.Join(dir, probeFileName)); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("probe file stat = %v", statErr)
	}
}

func TestMeasureProbeIssuesBlockOps(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, probeFileName)
	f, blocks, err := createProbe(path, 3)
	if err != nil {
		t.Fatalf("createProbe: %v", err)
	}
	defer func() {
		_ = f.Close()
		_ = os.Remove(path)
	}()
	if blocks != minProbeBlocks {
		t.Fatalf("blocks = %d, want %d", blocks, minProbeBlocks)
	}
	info, err := f.Stat()
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Size() != int64(minProbeBlocks*probeBlock) {
		t.Fatalf("size = %d", info.Size())
	}
	wrapped := &countingFile{probeIO: f}
	stats, err := measureProbe(wrapped, blocks, 3)
	if err != nil {
		t.Fatalf("measureProbe: %v", err)
	}
	if wrapped.reads != 3 || wrapped.writes != 3 || wrapped.syncs != 3 {
		t.Fatalf("reads/writes/syncs = %d/%d/%d", wrapped.reads, wrapped.writes, wrapped.syncs)
	}
	if stats.Read.Ops != 3 || stats.Write.Ops != 3 {
		t.Fatalf("stats ops = %d/%d", stats.Read.Ops, stats.Write.Ops)
	}
}

func TestProbeBlockCount(t *testing.T) {
	if n, err := probePlan(1); err != nil || n != 256 {
		t.Fatalf("ops 1 = %d, %v", n, err)
	}
	if n, err := probePlan(200); err != nil || n != 400 {
		t.Fatalf("ops 200 = %d, %v", n, err)
	}
	if _, err := probePlan(0); err == nil {
		t.Fatal("ops 0 was accepted")
	}
}

func TestProbeRejectsMissingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing")
	if _, err := Probe(dir, 1, "no", false, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("missing bench dir was accepted")
	}
}

func assertProbeLines(t *testing.T, text string, ops int, purge string) {
	t.Helper()
	want := []string{
		"probe_ops: " + strconv.Itoa(ops),
		"probe_block: 4096",
		"probe_purge: " + purge,
		"read_iops: ",
		"read_lat_ms: ",
		"read_seconds: ",
		"write_iops: ",
		"write_lat_ms: ",
		"write_seconds: ",
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if len(lines) != len(want) {
		t.Fatalf("lines = %q", text)
	}
	for i, prefix := range want {
		if !strings.HasPrefix(lines[i], prefix) {
			t.Fatalf("line %d = %q, want prefix %q", i, lines[i], prefix)
		}
	}
}

type countingFile struct {
	probeIO
	reads  int
	writes int
	syncs  int
}

func (c *countingFile) ReadAt(p []byte, off int64) (int, error) {
	n, err := c.probeIO.ReadAt(p, off)
	if err == nil {
		c.reads++
	}
	return n, err
}

func (c *countingFile) WriteAt(p []byte, off int64) (int, error) {
	n, err := c.probeIO.WriteAt(p, off)
	if err == nil {
		c.writes++
	}
	return n, err
}

func (c *countingFile) Sync() error {
	if err := c.probeIO.Sync(); err != nil {
		return err
	}
	c.syncs++
	return nil
}
