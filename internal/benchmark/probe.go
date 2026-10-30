package benchmark

import (
	crand "crypto/rand"
	"errors"
	"fmt"
	"io"
	"math"
	mathrand "math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jiangxin/tiler/internal/durable"
)

const (
	probeBlock     = 4096
	probeFileName  = ".tiler-bench-probe"
	minProbeBlocks = 256
)

// ProbeStats is one 4 KiB random read pass and one write pass.
type ProbeStats struct {
	Ops   int
	Block int
	Purge string
	Read  ProbePass
	Write ProbePass
}

// ProbePass is the timing for one random 4 KiB pass.
// Ops is the number of completed block operations.
type ProbePass struct {
	Ops     int
	IOPS    float64
	AvgMs   float64
	Seconds float64
}

type probeIO interface {
	ReadAt(p []byte, off int64) (int, error)
	WriteAt(p []byte, off int64) (int, error)
	Sync() error
}

// Probe writes .tiler-bench-probe under dir, fsyncs it, samples ops
// random 4 KiB reads and then the same number of writes, and deletes
// the probe file. Each write is fsynced. purge is printed as
// probe_purge (empty means no). Format controls where lines go:
// table writes to stdout, tsv to stderr, and json writes nothing
// (the caller embeds ProbeStats). A nil writer uses the matching
// standard stream.
func Probe(dir string, ops int, purge string, format string, stdout, stderr io.Writer) (ProbeStats, error) {
	return probeDir(dir, ops, func() string { return purge }, format, stdout, stderr)
}

// probeDir fsyncs the probe file before purge runs, then measures.
// purge owns the cache drop so the sample is not taken while that
// file is still warm. A nil purge records probe_purge=no.
func probeDir(dir string, ops int, purge func() string, format string, stdout, stderr io.Writer) (ProbeStats, error) {
	path := filepath.Join(dir, probeFileName)
	f, blocks, err := createProbe(path, ops)
	if err != nil {
		return ProbeStats{}, err
	}
	defer func() {
		_ = f.Close()
		_ = os.Remove(path)
	}()
	label := ""
	if purge != nil {
		label = purge()
	}
	stats, err := measureProbe(f, blocks, ops)
	if err != nil {
		return ProbeStats{}, err
	}
	stats.Purge = purgeLabel(label)
	if format == FormatJSON {
		return stats, nil
	}
	if err := writeProbeLines(stats, format == FormatTSV, stdout, stderr); err != nil {
		return ProbeStats{}, err
	}
	return stats, nil
}

func purgeLabel(purge string) string {
	if strings.TrimSpace(purge) == "" {
		return "no"
	}
	return purge
}

func createProbe(path string, ops int) (*os.File, int, error) {
	blocks, err := probePlan(ops)
	if err != nil {
		return nil, 0, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, 0, fmt.Errorf("benchmark: create probe: %w", err)
	}
	if err := fillProbe(f, blocks); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return nil, 0, err
	}
	return f, blocks, nil
}

func probePlan(ops int) (int, error) {
	if ops < 1 {
		return 0, errors.New("benchmark: probe ops must be at least 1")
	}
	blocks := ops * 2
	if blocks < minProbeBlocks {
		return minProbeBlocks, nil
	}
	return blocks, nil
}

func fillProbe(f *os.File, blocks int) error {
	payload := make([]byte, probeBlock)
	if _, err := io.ReadFull(crand.Reader, payload); err != nil {
		return fmt.Errorf("benchmark: probe urandom: %w", err)
	}
	for range blocks {
		if _, err := f.Write(payload); err != nil {
			return fmt.Errorf("benchmark: probe write: %w", err)
		}
	}
	if err := durable.Sync(f); err != nil {
		return fmt.Errorf("benchmark: probe fsync: %w", err)
	}
	return nil
}

func measureProbe(f probeIO, blocks, ops int) (ProbeStats, error) {
	if err := probeReady(blocks, ops); err != nil {
		return ProbeStats{}, err
	}
	payload := make([]byte, probeBlock)
	if _, err := io.ReadFull(crand.Reader, payload); err != nil {
		return ProbeStats{}, fmt.Errorf("benchmark: probe urandom: %w", err)
	}
	read, err := timeProbePass(f, blocks, ops, nil)
	if err != nil {
		return ProbeStats{}, err
	}
	write, err := timeProbePass(f, blocks, ops, payload)
	if err != nil {
		return ProbeStats{}, err
	}
	return ProbeStats{Ops: ops, Block: probeBlock, Read: read, Write: write}, nil
}

func probeReady(blocks, ops int) error {
	if blocks < 1 || ops < 1 {
		return errors.New("benchmark: probe needs at least one block and one op")
	}
	return nil
}

func timeProbePass(f probeIO, blocks, ops int, payload []byte) (ProbePass, error) {
	buf := make([]byte, probeBlock)
	var latencies []time.Duration
	start := time.Now()
	for range ops {
		off := int64(mathrand.IntN(blocks)) * probeBlock
		opStart := time.Now()
		if err := probeOnce(f, buf, payload, off); err != nil {
			return ProbePass{}, err
		}
		latencies = append(latencies, time.Since(opStart))
	}
	return passStats(time.Since(start), latencies), nil
}

func probeOnce(f probeIO, buf, payload []byte, off int64) error {
	if payload == nil {
		return probeRead(f, buf, off)
	}
	return probeWrite(f, payload, off)
}

func probeRead(f probeIO, buf []byte, off int64) error {
	n, err := f.ReadAt(buf, off)
	if err != nil {
		return fmt.Errorf("benchmark: probe read: %w", err)
	}
	if n != probeBlock {
		return errors.New("benchmark: probe short read")
	}
	return nil
}

func probeWrite(f probeIO, payload []byte, off int64) error {
	n, err := f.WriteAt(payload, off)
	if err != nil {
		return fmt.Errorf("benchmark: probe write: %w", err)
	}
	if n != probeBlock {
		return errors.New("benchmark: probe short write")
	}
	if err := durable.Sync(f); err != nil {
		return fmt.Errorf("benchmark: probe fsync: %w", err)
	}
	return nil
}

func passStats(elapsed time.Duration, latencies []time.Duration) ProbePass {
	seconds := elapsed.Seconds()
	iops := math.Inf(1)
	if seconds > 0 {
		iops = float64(len(latencies)) / seconds
	}
	return ProbePass{
		Ops:     len(latencies),
		IOPS:    iops,
		AvgMs:   averageMs(latencies),
		Seconds: seconds,
	}
}

func averageMs(latencies []time.Duration) float64 {
	if len(latencies) == 0 {
		return 0
	}
	var sum time.Duration
	for _, d := range latencies {
		sum += d
	}
	return sum.Seconds() * 1000 / float64(len(latencies))
}

func writeProbeLines(stats ProbeStats, tsv bool, stdout, stderr io.Writer) error {
	w := probeOut(tsv, stdout, stderr)
	for _, line := range probeLines(stats) {
		if _, err := io.WriteString(w, line); err != nil {
			return fmt.Errorf("benchmark: write probe: %w", err)
		}
	}
	return nil
}

func probeOut(tsv bool, stdout, stderr io.Writer) io.Writer {
	if tsv {
		if stderr != nil {
			return stderr
		}
		return os.Stderr
	}
	if stdout != nil {
		return stdout
	}
	return os.Stdout
}

func probeLines(stats ProbeStats) []string {
	return []string{
		fmt.Sprintf("probe_ops: %d\n", stats.Ops),
		fmt.Sprintf("probe_block: %d\n", stats.Block),
		fmt.Sprintf("probe_purge: %s\n", stats.Purge),
		fmt.Sprintf("read_iops: %s\n", formatFixed(stats.Read.IOPS, 1)),
		fmt.Sprintf("read_lat_ms: %s\n", formatFixed(stats.Read.AvgMs, 3)),
		fmt.Sprintf("read_seconds: %s\n", formatFixed(stats.Read.Seconds, 3)),
		fmt.Sprintf("write_iops: %s\n", formatFixed(stats.Write.IOPS, 1)),
		fmt.Sprintf("write_lat_ms: %s\n", formatFixed(stats.Write.AvgMs, 3)),
		fmt.Sprintf("write_seconds: %s\n", formatFixed(stats.Write.Seconds, 3)),
	}
}

func formatFixed(v float64, prec int) string {
	if math.IsInf(v, 0) {
		return "inf"
	}
	return strconv.FormatFloat(v, 'f', prec, 64)
}
