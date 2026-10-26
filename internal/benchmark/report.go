package benchmark

import (
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
// negative Seconds with a numerator prints inf.
type Step struct {
	Name      string
	Seconds   float64
	Bytes     *int64
	RateBytes *int64
	Notes     string
}

// Reporter writes the human step table or --tsv rows.
// Probe lines are not step rows. Probe sends those lines to stderr
// when tsv is set.
type Reporter struct {
	tsv bool
	w   io.Writer
}

// NewReporter writes the human header unless tsv is set.
// A nil writer defaults to standard output.
func NewReporter(tsv bool, w io.Writer) (*Reporter, error) {
	if w == nil {
		w = os.Stdout
	}
	r := &Reporter{tsv: tsv, w: w}
	if tsv {
		return r, nil
	}
	if err := r.writeHumanHeader(); err != nil {
		return nil, err
	}
	return r, nil
}

// Row prints one step. Column order is step, seconds, bytes, MiB/s,
// notes. --tsv rows are tab-separated and have no header.
func (r *Reporter) Row(step Step) error {
	sec := strconv.FormatFloat(step.Seconds, 'f', 3, 64)
	return r.writeColumns(step.Name, sec, step.byteText(), step.rateText(), step.Notes)
}

func (r *Reporter) writeHumanHeader() error {
	if err := r.writeColumns("step", "seconds", "bytes", "MiB/s", "notes"); err != nil {
		return err
	}
	return r.writeColumns(dashes(18), dashes(10), dashes(12), dashes(10), dashes(12))
}

func (r *Reporter) writeColumns(step, sec, nbytes, rate, notes string) error {
	var err error
	if r.tsv {
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

func dashes(n int) string {
	return strings.Repeat("-", n)
}
