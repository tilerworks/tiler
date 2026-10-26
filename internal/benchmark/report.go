package benchmark

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// Step is one row of the benchmark table.
// Bytes is the bytes column. A nil Bytes prints "-".
// RateBytes is the MiB/s numerator. A nil RateBytes uses Bytes.
// image_create shows the output file size in Bytes and the payload
// size in RateBytes. image_attach leaves both nil. A zero or
// negative Seconds with a numerator prints inf for table/tsv, and
// a null mib_s for json.
type Step struct {
	Name      string
	Seconds   float64
	Bytes     *int64
	RateBytes *int64
	Notes     string
}

// Reporter writes the human step table, --format tsv rows, or
// buffers steps for --format json. Probe lines are not step rows.
type Reporter struct {
	format string
	w      io.Writer
	steps  []Step
}

// NewReporter writes the human header for table format.
// A nil writer defaults to standard output.
func NewReporter(format string, w io.Writer) (*Reporter, error) {
	if w == nil {
		w = os.Stdout
	}
	if format == "" {
		format = FormatTable
	}
	r := &Reporter{format: format, w: w}
	if format != FormatTable {
		return r, nil
	}
	if err := r.writeHumanHeader(); err != nil {
		return nil, err
	}
	return r, nil
}

// Row prints one step for table/tsv, or buffers it for json.
// Column order is step, seconds, bytes, MiB/s, notes.
func (r *Reporter) Row(step Step) error {
	if r.format == FormatJSON {
		r.steps = append(r.steps, step)
		return nil
	}
	sec := strconv.FormatFloat(step.Seconds, 'f', 3, 64)
	return r.writeColumns(step.Name, sec, step.byteText(), step.rateText(), step.Notes)
}

// Steps returns the buffered steps for json format.
func (r *Reporter) Steps() []Step {
	return r.steps
}

func (r *Reporter) writeHumanHeader() error {
	if err := r.writeColumns("step", "seconds", "bytes", "MiB/s", "notes"); err != nil {
		return err
	}
	return r.writeColumns(dashes(18), dashes(10), dashes(12), dashes(10), dashes(12))
}

func (r *Reporter) writeColumns(step, sec, nbytes, rate, notes string) error {
	var err error
	if r.format == FormatTSV {
		_, err = fmt.Fprintf(r.w, "%s\t%s\t%s\t%s\t%s\n", step, sec, nbytes, rate, notes)
	} else {
		_, err = fmt.Fprintf(r.w, "%-18s %10s %12s %10s %12s\n", step, sec, nbytes, rate, notes)
	}
	if err != nil {
		return fmt.Errorf("benchmark: write report: %w", err)
	}
	return nil
}

func (s Step) byteText() string {
	if s.Bytes == nil {
		return "-"
	}
	return strconv.FormatInt(*s.Bytes, 10)
}

func (s Step) rateText() string {
	n, ok := s.rateNumerator()
	if !ok {
		return "-"
	}
	if s.Seconds <= 0 {
		return "inf"
	}
	per := float64(n) / s.Seconds / float64(chunkSize)
	return strconv.FormatFloat(per, 'f', 1, 64)
}

func (s Step) rateNumerator() (int64, bool) {
	if s.RateBytes != nil {
		return *s.RateBytes, true
	}
	if s.Bytes != nil {
		return *s.Bytes, true
	}
	return 0, false
}

// mibPerSec returns the MiB/s value for json, or nil when there is
// no numerator or seconds is not positive (encoding/json cannot
// emit Infinity).
func (s Step) mibPerSec() *float64 {
	n, ok := s.rateNumerator()
	if !ok || s.Seconds <= 0 {
		return nil
	}
	per := float64(n) / s.Seconds / float64(chunkSize)
	return &per
}

func dashes(n int) string {
	return strings.Repeat("-", n)
}

// jsonReport is the single object --format json writes on success.
type jsonReport struct {
	OS           string     `json:"os"`
	Type         string     `json:"type"`
	Path         string     `json:"path"`
	PathWritable bool       `json:"path_writable"`
	RuntimeDir   string     `json:"runtime_dir"`
	Mode         string     `json:"mode"`
	Size         int64      `json:"size"`
	FileSize     int64      `json:"file_size"`
	Compress     string     `json:"compress,omitempty"`
	ProbeEnabled bool       `json:"probe_enabled"`
	Device       deviceInfo `json:"device"`
	Probe        *jsonProbe `json:"probe,omitempty"`
	Steps        []jsonStep `json:"steps"`
}

type deviceInfo struct {
	VolumeDevice string `json:"volume_device"`
	DF           string `json:"df"`
	DeviceID     string `json:"device_id,omitempty"`
	VolumeName   string `json:"volume_name,omitempty"`
	MountPoint   string `json:"mount_point,omitempty"`
}

type jsonProbe struct {
	Ops   int           `json:"ops"`
	Block int           `json:"block"`
	Purge string        `json:"purge"`
	Read  jsonProbePass `json:"read"`
	Write jsonProbePass `json:"write"`
}

type jsonProbePass struct {
	IOPS    float64 `json:"iops"`
	LatMs   float64 `json:"lat_ms"`
	Seconds float64 `json:"seconds"`
}

type jsonStep struct {
	Step      string   `json:"step"`
	Seconds   float64  `json:"seconds"`
	Bytes     *int64   `json:"bytes"`
	RateBytes *int64   `json:"rate_bytes"`
	MiBS      *float64 `json:"mib_s"`
	Notes     string   `json:"notes"`
}

func writeJSONReport(d Decision, runtimeDir string, device deviceInfo, probe *ProbeStats, steps []Step, w io.Writer) error {
	if w == nil {
		w = os.Stdout
	}
	rep := jsonReport{
		OS:           goosName(),
		Type:         d.Type,
		Path:         d.Path,
		PathWritable: dirWritable(d.Path),
		RuntimeDir:   runtimeDir,
		Mode:         d.Mode,
		Size:         d.Size,
		FileSize:     d.FileSize,
		Compress:     d.Compress,
		ProbeEnabled: d.Probe,
		Device:       device,
		Steps:        make([]jsonStep, 0, len(steps)),
	}
	if probe != nil {
		rep.Probe = &jsonProbe{
			Ops:   probe.Ops,
			Block: probe.Block,
			Purge: probe.Purge,
			Read: jsonProbePass{
				IOPS:    probe.Read.IOPS,
				LatMs:   probe.Read.AvgMs,
				Seconds: probe.Read.Seconds,
			},
			Write: jsonProbePass{
				IOPS:    probe.Write.IOPS,
				LatMs:   probe.Write.AvgMs,
				Seconds: probe.Write.Seconds,
			},
		}
	}
	for _, s := range steps {
		js := jsonStep{
			Step:      s.Name,
			Seconds:   s.Seconds,
			Bytes:     s.Bytes,
			RateBytes: s.RateBytes,
			MiBS:      s.mibPerSec(),
			Notes:     s.Notes,
		}
		if js.RateBytes == nil && s.Bytes != nil {
			js.RateBytes = s.Bytes
		}
		rep.Steps = append(rep.Steps, js)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(rep); err != nil {
		return fmt.Errorf("benchmark: write json: %w", err)
	}
	return nil
}
