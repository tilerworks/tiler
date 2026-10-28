package cli

import (
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// failWriter fails every write, standing in for a closed or broken stream.
type failWriter struct{ err error }

// Write reports the injected failure.
func (w failWriter) Write([]byte) (int, error) { return 0, w.err }

// failReader fails every read with a real I/O error, not an end of input.
type failReader struct{ err error }

// Read reports the injected failure.
func (r failReader) Read([]byte) (int, error) { return 0, r.err }

// fillerReader hands out filler bytes without a line ending and counts what the
// confirmation actually consumed, so a bounded read can be asserted without a
// reader that never returns.
type fillerReader struct {
	left, read int
}

// Read fills the buffer with filler and reports the end of the input.
func (r *fillerReader) Read(p []byte) (int, error) {
	if r.left == 0 {
		return 0, io.EOF
	}
	n := min(len(p), r.left)
	for i := range p[:n] {
		p[i] = '.'
	}
	r.left -= n
	r.read += n
	return n, nil
}

// recordingStream appends every message to one buffer, so a test can assert
// the order of the listing and the question.
type recordingStream struct{ body strings.Builder }

// Write appends one message.
func (s *recordingStream) Write(p []byte) (int, error) { return s.body.Write(p) }

// String returns everything written so far.
func (s *recordingStream) String() string { return s.body.String() }

// withTerminalSeam replaces the terminal check of one test.
func withTerminalSeam(t *testing.T, terminal bool) {
	t.Helper()
	original := inputTerminal
	t.Cleanup(func() { inputTerminal = original })
	inputTerminal = func(io.Reader) bool { return terminal }
}

// runExpireFlag executes a stub gc command carrying the shared --expire flag
// and reports the window its handler resolves.
func runExpireFlag(t *testing.T, args ...string) (time.Duration, error) {
	t.Helper()
	var raw string
	var window time.Duration
	var windowErr error
	cmd := &cobra.Command{Use: "gc", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			window, windowErr = expireWindow(raw)
			return windowErr
		}}
	bindExpireFlag(cmd, &raw)
	cmd.SetArgs(args)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err != nil {
		return 0, err
	}
	return window, windowErr
}

func TestExpireWindowAcceptsWholeDays(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want time.Duration
	}{
		{"0", 0},
		{"1", 24 * time.Hour},
		{"30", 30 * 24 * time.Hour},
		{strconv.FormatInt(maxExpireDays, 10), time.Duration(maxExpireDays) * expireDay},
	} {
		got, err := expireWindow(tc.raw)
		if err != nil || got != tc.want {
			t.Fatalf("expireWindow(%q) = %v, %v; want %v", tc.raw, got, err, tc.want)
		}
	}
}

func TestExpireWindowRejectsInvalidDays(t *testing.T) {
	for _, raw := range []string{
		"", "-1", "+1", "1.5", "1.0", " 30", "30 ", "30d", "thirty",
		strconv.FormatInt(maxExpireDays+1, 10), "99999999999999999999999",
	} {
		window, err := expireWindow(raw)
		if err == nil || window != 0 {
			t.Fatalf("expireWindow(%q) = %v, %v; want a usage error", raw, window, err)
		}
		if code := ExitCode(err); code != 2 {
			t.Fatalf("exit code of %q = %d, want the usage code 2", raw, code)
		}
	}
}

func TestExpireFlagSharesTheDefaultAndZero(t *testing.T) {
	if window, err := runExpireFlag(t); err != nil || window != 30*24*time.Hour {
		t.Fatalf("default window = %v, %v; want %v", window, err, 30*24*time.Hour)
	}
	if window, err := runExpireFlag(t, "--expire", "0"); err != nil || window != 0 {
		t.Fatalf("zero window = %v, %v; want immediate eligibility", window, err)
	}
	if _, err := runExpireFlag(t, "--expire", "many"); err == nil || ExitCode(err) != 2 {
		t.Fatalf("invalid window error = %v, want a usage error", err)
	}
}

func TestConfirmationListsBeforeItApproves(t *testing.T) {
	withTerminalSeam(t, true)
	stream := &recordingStream{}
	confirmation := gcConfirmation{
		Preview: gcPreview{
			Title:  "Expired sessions:",
			Rows:   []string{"  dev  2026-09-01T00:00:00Z  57 days"},
			Notice: "Removing a session deletes its runtime/ upper data.",
		},
		In: strings.NewReader("yes\n"), Out: stream, ErrOut: stream, Objects: "sessions",
	}
	approved, err := confirmation.approved()
	if err != nil || !approved {
		t.Fatalf("approved = %v, %v; want the confirmed answer", approved, err)
	}
	text := stream.String()
	row, notice, question := strings.Index(text, "  dev  "), strings.Index(text, "runtime/"), strings.Index(text, "Type yes")
	if row < 0 || notice < row || question < notice {
		t.Fatalf("transcript %q does not list the candidate, its effect, then the question", text)
	}
	if strings.Count(text, "Type yes") != 1 {
		t.Fatalf("question asked %d times in %q, want once", strings.Count(text, "Type yes"), text)
	}
}

func TestConfirmationAcceptsOnlyLowercaseYes(t *testing.T) {
	withTerminalSeam(t, true)
	for _, tc := range []struct {
		answer string
		want   bool
	}{
		{"yes\n", true},
		{"yes\r\n", true},
		{"y\n", false},
		{"YES\n", false},
		{"Yes\n", false},
		{" yes\n", false},
		{"yes \n", false},
		{"\n", false},
		{"no\n", false},
		{"yes", false},
		{"", false},
	} {
		confirmation := testConfirmation(t, strings.NewReader(tc.answer), io.Discard, io.Discard)
		approved, err := confirmation.approved()
		if err != nil || approved != tc.want {
			t.Fatalf("answer %q approved = %v, %v; want %v", tc.answer, approved, err, tc.want)
		}
	}
}

func TestConfirmationStopsReadingAnAnswerThatNeverEnds(t *testing.T) {
	withTerminalSeam(t, true)
	input := &fillerReader{left: 1 << 20}
	confirmation := testConfirmation(t, input, io.Discard, io.Discard)
	approved, err := confirmation.approved()
	if err != nil || approved {
		t.Fatalf("endless answer approved = %v, %v; want a cancel", approved, err)
	}
	if input.read > maxAnswerBytes {
		t.Fatalf("confirmation read %d bytes, want at most %d", input.read, maxAnswerBytes)
	}
}

func TestConfirmationNeverAsksWithoutCandidates(t *testing.T) {
	withTerminalSeam(t, true)
	stream := &recordingStream{}
	confirmation := gcConfirmation{
		Preview: gcPreview{Title: "Expired sessions:", Notice: "Removing a session deletes its runtime/ upper data."},
		In:      failReader{errors.New("no question may be asked")},
		Out:     stream, ErrOut: stream, Objects: "sessions",
	}
	approved, err := confirmation.approved()
	if err != nil || approved {
		t.Fatalf("empty listing approved = %v, %v; want no approval and no error", approved, err)
	}
	if text := stream.String(); strings.Contains(text, "Type yes") || strings.Contains(text, "runtime/") {
		t.Fatalf("empty listing printed %q, want the title only", text)
	}
}

func TestConfirmationYesSkipsTheQuestion(t *testing.T) {
	withTerminalSeam(t, false)
	stream := &recordingStream{}
	confirmation := testConfirmation(t, failReader{errors.New("input must not be read")}, stream, stream)
	confirmation.AssumeYes = true
	approved, err := confirmation.approved()
	if err != nil || !approved {
		t.Fatalf("--yes approved = %v, %v; want approval without a question", approved, err)
	}
	if text := stream.String(); !strings.Contains(text, "  dev  ") || strings.Contains(text, "Type yes") {
		t.Fatalf("transcript %q does not list the candidate without asking", text)
	}
}

func TestConfirmationNeverConsumesNoninteractiveInput(t *testing.T) {
	withTerminalSeam(t, false)
	errOut := &strings.Builder{}
	confirmation := testConfirmation(t, failReader{errors.New("piped input must not be read")}, io.Discard, errOut)
	approved, err := confirmation.approved()
	if err != nil || approved {
		t.Fatalf("piped yes approved = %v, %v; want a refusal without reading", approved, err)
	}
	if advice := errOut.String(); !strings.Contains(advice, "--yes") {
		t.Fatalf("advice = %q, want the --yes hint", advice)
	}
}

func TestConfirmationReportsStreamFailures(t *testing.T) {
	failure := errors.New("stream closed")
	for _, tc := range []struct {
		name     string
		terminal bool
		build    func(*gcConfirmation)
		want     string
	}{
		{name: "preview", terminal: true, want: "preview",
			build: func(c *gcConfirmation) { c.Out = failWriter{failure} }},
		{name: "prompt", terminal: true, want: "prompt",
			build: func(c *gcConfirmation) { c.ErrOut = failWriter{failure} }},
		{name: "advice", want: "advice",
			build: func(c *gcConfirmation) { c.ErrOut = failWriter{failure} }},
		{name: "read", terminal: true, want: "read confirmation",
			build: func(c *gcConfirmation) { c.In = failReader{failure} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withTerminalSeam(t, tc.terminal)
			confirmation := testConfirmation(t, strings.NewReader("yes\n"), io.Discard, io.Discard)
			tc.build(&confirmation)
			approved, err := confirmation.approved()
			if approved || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("approved = %v, err = %v; want the %s failure and no approval",
					approved, err, tc.want)
			}
		})
	}
}

// testConfirmation builds one confirmation over the injected streams.
func testConfirmation(t *testing.T, in io.Reader, out, errOut io.Writer) gcConfirmation {
	t.Helper()
	return gcConfirmation{
		Preview: gcPreview{
			Title:  "Expired sessions:",
			Rows:   []string{"  dev  2026-09-01T00:00:00Z  57 days"},
			Notice: "Removing a session deletes its runtime/ upper data.",
		},
		In: in, Out: out, ErrOut: errOut, Objects: "sessions",
	}
}
