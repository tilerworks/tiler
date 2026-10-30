package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// defaultExpireDays is the retention both gc commands share when --expire is
// omitted. Zero is a valid value and expires every inactive object, which
// differs from the zero of the older configuration fields that disabled the
// cleanup.
const defaultExpireDays = 30

// expireDay is the only unit of the retention window: --expire counts whole
// days and a day is always 24 hours.
const expireDay = 24 * time.Hour

// maxExpireDays is the largest whole day count a time.Duration holds.
const maxExpireDays = int64(math.MaxInt64 / int64(expireDay))

// maxAnswerBytes bounds the confirmation read. An answer is a few bytes, and a
// character device that never ends a line must cancel instead of growing the
// read buffer without limit.
const maxAnswerBytes = 512

// bindExpireFlag registers the shared --expire flag on one gc command. The
// value stays a string: an integer flag would wrap an oversized day count and
// accept a sign, while both commands must refuse such input as a usage error.
func bindExpireFlag(cmd *cobra.Command, raw *string) {
	cmd.Flags().StringVar(raw, "expire", strconv.Itoa(defaultExpireDays),
		"inactive days before an object expires (default 30; zero expires every inactive object)")
}

// expireWindow converts --expire into the retention window. Only plain decimal
// digits are accepted, so a fractional, signed, spaced, or suffixed value is a
// usage error instead of a silently truncated number. The day count is checked
// against the duration range before the multiplication can overflow.
func expireWindow(raw string) (time.Duration, error) {
	days, err := parseExpireDays(raw)
	if err != nil {
		return 0, usageError{err}
	}
	if days > maxExpireDays {
		return 0, usageError{fmt.Errorf(
			"--expire %s exceeds the largest window of %d days", raw, maxExpireDays)}
	}
	return time.Duration(days) * expireDay, nil
}

// parseExpireDays reads the decimal day count of --expire.
func parseExpireDays(raw string) (int64, error) {
	if raw == "" {
		return 0, errors.New("--expire needs a number of days")
	}
	for _, digit := range raw {
		if digit < '0' || digit > '9' {
			return 0, fmt.Errorf("--expire %q is not a nonnegative whole number of days", raw)
		}
	}
	days, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("--expire %q is not a nonnegative whole number of days", raw)
	}
	return days, nil
}

// gcPreview is the candidate listing a gc command prints before any removal.
// The command renders its own columns; the shared helper owns the order of
// listing, notice, and question, so no removal is authorized before the
// listing reached the user.
type gcPreview struct {
	// Title opens the listing and names the columns.
	Title string
	// Rows render one candidate each.
	Rows []string
	// Sections replaces Title and Rows when a listing needs more than one
	// heading. Each heading describes only its own rows, and headings are
	// not candidates.
	Sections []gcSection
	// Notice explains what removing the rows destroys, for example the
	// session runtime and its upper data. It prints only with a candidate.
	Notice string
}

// gcSection is one headed group of candidates inside a preview.
type gcSection struct {
	Title string
	Rows  []string
}

// listing is the headed groups to print. A preview without sections is one
// group, so session GC keeps a single title.
func (p gcPreview) listing() []gcSection {
	if len(p.Sections) > 0 {
		return p.Sections
	}
	return []gcSection{{Title: p.Title, Rows: p.Rows}}
}

// candidateCount is the number of removable objects, ignoring headings.
func (p gcPreview) candidateCount() int {
	count := 0
	for _, section := range p.listing() {
		count += len(section.Rows)
	}
	return count
}

// write prints the listing and reports the first write failure.
func (p gcPreview) write(out io.Writer) error {
	for i, section := range p.listing() {
		if i > 0 {
			if _, err := fmt.Fprintln(out); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(out, section.Title); err != nil {
			return err
		}
		for _, row := range section.Rows {
			if _, err := fmt.Fprintln(out, row); err != nil {
				return err
			}
		}
	}
	if p.candidateCount() == 0 || p.Notice == "" {
		return nil
	}
	_, err := fmt.Fprintln(out, p.Notice)
	return err
}

// gcConfirmation shows one preview and reports whether the caller may remove
// the listed objects. Input, output streams, the --yes bypass, and the
// terminal check are injected, so both gc commands share one confirmation
// contract and tests drive every branch without a terminal.
type gcConfirmation struct {
	// Preview is the listing printed before any question.
	Preview gcPreview
	// In is the standard input the command resolved through Cobra.
	In io.Reader
	// Out receives the listing; ErrOut receives the question and the advice.
	Out, ErrOut io.Writer
	// Objects names the candidates in the question, for example "sessions".
	Objects string
	// AssumeYes reports that --yes was given.
	AssumeYes bool
	// DryRun prevents approval even when --yes is given.
	DryRun bool
}

// approved prints the preview and reports whether removal may proceed. An
// empty listing never asks and never approves. --yes approves only after the
// listing is printed. Without --yes a noninteractive input prints the advice
// to rerun with --yes and approves nothing, so a piped yes cannot authorize
// removal. A failed write or read is an error, and an error never approves.
func (c gcConfirmation) approved() (bool, error) {
	if err := c.Preview.write(c.Out); err != nil {
		return false, fmt.Errorf("gc: preview: %w", err)
	}
	if c.DryRun {
		if _, err := fmt.Fprintln(c.Out, "Dry run: nothing removed."); err != nil {
			return false, fmt.Errorf("gc: dry-run notice: %w", err)
		}
		return false, nil
	}
	if c.Preview.candidateCount() == 0 {
		return false, nil
	}
	if c.AssumeYes {
		return true, nil
	}
	if !inputTerminal(c.In) {
		_, err := fmt.Fprintf(c.ErrOut,
			"Nothing removed: confirmation needs an interactive terminal; rerun with --yes to remove %d %s.\n",
			c.Preview.candidateCount(), c.Objects)
		if err != nil {
			return false, fmt.Errorf("gc: advice: %w", err)
		}
		return false, nil
	}
	return c.read()
}

// read asks once and accepts only the lowercase yes after the line ending. A
// refusal, an empty answer, and an end of input cancel without an error; only
// a real read failure is a runtime error. The read is bounded, so a terminal
// check that accepted a never-answering device still ends in a cancel.
func (c gcConfirmation) read() (bool, error) {
	if _, err := fmt.Fprintf(c.ErrOut, "Type yes to remove %d %s: ",
		c.Preview.candidateCount(), c.Objects); err != nil {
		return false, fmt.Errorf("gc: prompt: %w", err)
	}
	answer, err := bufio.NewReader(io.LimitReader(c.In, maxAnswerBytes)).ReadString('\n')
	if errors.Is(err, io.EOF) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("gc: read confirmation: %w", err)
	}
	return strings.TrimRight(answer, "\r\n") == "yes", nil
}
