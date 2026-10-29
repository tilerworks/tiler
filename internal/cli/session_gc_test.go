package cli

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/activity"
	"github.com/jiangxin/tiler/internal/session"
)

// gcClock is the mutable clock the gc command tests share with every
// engine the session seam builds: seeding moves it into the past so
// the next scan expires what the seed created, and the scan itself
// runs on the present again.
type gcClock struct{ now time.Time }

// Now answers one engine clock call.
func (c *gcClock) Now() time.Time { return c.now }

// age moves the clock the given duration into the past.
func (c *gcClock) age(d time.Duration) { c.now = time.Now().UTC().Add(-d) }

// reset moves the clock back to the present.
func (c *gcClock) reset() { c.now = time.Now().UTC() }

// withSessionGCSeams installs an empty mount table, the no-op session
// lock, and the mutable engine clock, following the shared seam
// discipline of the other command tests.
func withSessionGCSeams(t *testing.T) *gcClock {
	t.Helper()
	withMountSeams(t, nil)
	clock := &gcClock{now: time.Now().UTC()}
	withSessionSeams(t, func(e *session.Engine) { e.Now = clock.Now })
	return clock
}

// gcArgs builds one session gc command line over the library flags.
func gcArgs(lib []string, extra ...string) []string {
	args := append(append([]string{}, lib...), "session", "gc")
	return append(args, extra...)
}

// newGCSession creates one session record through the command under
// the shared clock, so the record and its activity log carry the
// clock's time.
func newGCSession(t *testing.T, lib []string, name string, removable bool) {
	t.Helper()
	args := append(append([]string{}, lib...), "session", "new")
	if removable {
		args = append(args, "--rm")
	}
	if _, stderr, _, err := runCmd(t, append(args, name)...); err != nil {
		t.Fatalf("session new %s: %v\n%s", name, err, stderr)
	}
}

// ageSessions creates each named session with its whole activity 90
// days in the past and then resets the clock to the present.
func ageSessions(t *testing.T, lib []string, clock *gcClock, names ...string) {
	t.Helper()
	clock.age(90 * 24 * time.Hour)
	for _, name := range names {
		newGCSession(t, lib, name, false)
	}
	clock.reset()
}

// seedLegacyRecord writes one bare record with no activity log, the
// shape a session directory has before the activity log existed.
func seedLegacyRecord(t *testing.T, home, name string) {
	t.Helper()
	store := session.Store{Dir: sessionsDir(home)}
	rec := session.Record{Name: name, State: session.StateClosed,
		CreatedAt: time.Now().UTC().AddDate(0, 0, -90)}
	if err := store.Save(rec); err != nil {
		t.Fatal(err)
	}
}

// sessionExists reports whether the session directory is still on
// disk.
func sessionExists(home, name string) bool {
	_, err := os.Stat(filepath.Join(sessionsDir(home), name))
	return err == nil
}

// confirmReader answers the confirmation question after running one
// mutation, standing in for a concurrent process that changes the
// store between the preview and the answer.
type confirmReader struct {
	mutate func()
	answer string
	served bool
}

// Read runs the mutation once and then serves the answer.
func (r *confirmReader) Read(p []byte) (int, error) {
	if r.served {
		return 0, io.EOF
	}
	r.served = true
	if r.mutate != nil {
		r.mutate()
	}
	return copy(p, r.answer), nil
}

func TestSessionGCHelpAdvertisesTheSharedFlags(t *testing.T) {
	_, _ = withWorkspace(t)
	for _, command := range []string{"session", "sessions"} {
		stdout, stderr, _, err := runCmd(t, command, "gc", "--help")
		if err != nil {
			t.Fatalf("%s gc --help: %v\n%s", command, err, stderr)
		}
		for _, want := range []string{"--expire", "default 30", "--yes", "runtime/"} {
			if !strings.Contains(stdout, want) {
				t.Fatalf("%s gc help missing %q:\n%s", command, want, stdout)
			}
		}
	}
}

func TestSessionGCRejectsUsageErrors(t *testing.T) {
	home, _ := withWorkspace(t)
	_, lib := libFlags(home)
	clock := withSessionGCSeams(t)
	ageSessions(t, lib, clock, "aged")
	for _, args := range [][]string{
		{"session", "gc", "extra"},
		{"session", "gc", "--expire", "-1"},
		{"session", "gc", "--expire=abc"},
		{"session", "gc", "--expire=1.5"},
	} {
		_, _, _, err := runCmd(t, append(append([]string{}, lib...), args...)...)
		if ExitCode(err) != 2 {
			t.Fatalf("gc %v: ExitCode(%v) = %d, want the usage code 2",
				args, err, ExitCode(err))
		}
	}
	if !sessionExists(home, "aged") {
		t.Fatal("a usage error removed the expired session")
	}
}

// TestSessionGCPreviewsWithoutRemoving pins the acceptance rule: a
// noninteractive run without --yes lists the candidates with their
// last activity and the runtime/upper effect, advises the flag, and
// removes nothing — an old --rm record included.
func TestSessionGCPreviewsWithoutRemoving(t *testing.T) {
	home, _ := withWorkspace(t)
	_, lib := libFlags(home)
	clock := withSessionGCSeams(t)
	clock.age(90 * 24 * time.Hour)
	newGCSession(t, lib, "aged", false)
	newGCSession(t, lib, "aged-rm", true)
	clock.reset()
	withTerminalSeam(t, false)

	stdout, stderr, _, err := runCmd(t, gcArgs(lib)...)
	if err != nil {
		t.Fatalf("session gc: %v\n%s", err, stderr)
	}
	for _, want := range []string{
		"Expired sessions (last activity before",
		"\n  aged ", "\n  aged-rm ", "last active", "--rm",
		"runtime/", "upper data",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("listing missing %q:\n%s", want, stdout)
		}
	}
	if strings.Index(stdout, "\n  aged ") >= strings.Index(stdout, "\n  aged-rm ") {
		t.Fatalf("listing is not in name order:\n%s", stdout)
	}
	if !strings.Contains(stderr, "--yes") || strings.Contains(stderr, "Type yes") {
		t.Fatalf("stderr = %q, want the --yes advice and no question", stderr)
	}
	for _, name := range []string{"aged", "aged-rm"} {
		if !sessionExists(home, name) {
			t.Fatalf("the noninteractive preview removed %s", name)
		}
	}
}

func TestSessionGCRemovesListedSessionsAfterYes(t *testing.T) {
	home, _ := withWorkspace(t)
	_, lib := libFlags(home)
	clock := withSessionGCSeams(t)
	clock.age(90 * 24 * time.Hour)
	newGCSession(t, lib, "aged", false)
	newGCSession(t, lib, "aged-rm", true)
	clock.reset()
	withTerminalSeam(t, true)

	stdout, stderr, err := runCmdReader(t, strings.NewReader("yes\n"), gcArgs(lib)...)
	if err != nil {
		t.Fatalf("session gc: %v\n%s", err, stderr)
	}
	if strings.Count(stderr, "Type yes to remove 2 sessions: ") != 1 {
		t.Fatalf("stderr = %q, want the question exactly once", stderr)
	}
	for _, want := range []string{
		"removed aged\n", "removed aged-rm\n",
		"removed 2 of 2 approved sessions, kept 0\n",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("report missing %q:\n%s", want, stdout)
		}
	}
	for _, name := range []string{"aged", "aged-rm"} {
		if sessionExists(home, name) {
			t.Fatalf("the confirmed gc kept %s", name)
		}
	}
}

func TestSessionGCYesFlagRemovesWithoutAsking(t *testing.T) {
	home, _ := withWorkspace(t)
	_, lib := libFlags(home)
	clock := withSessionGCSeams(t)
	ageSessions(t, lib, clock, "aged")
	withTerminalSeam(t, false)

	stdout, stderr, _, err := runCmd(t, gcArgs(lib, "--yes")...)
	if err != nil {
		t.Fatalf("session gc --yes: %v\n%s", err, stderr)
	}
	if strings.Contains(stderr, "Type yes") {
		t.Fatalf("stderr = %q, want no question with --yes", stderr)
	}
	for _, want := range []string{
		"Expired sessions (last activity before", "removed aged\n",
		"removed 1 of 1 approved sessions, kept 0\n",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("output missing %q:\n%s", want, stdout)
		}
	}
	if sessionExists(home, "aged") {
		t.Fatal("--yes kept the expired session")
	}
}

func TestSessionGCCancelsOnAnyOtherAnswer(t *testing.T) {
	for i, answer := range []string{"y\n", "YES\n", "Yes\n", " yes\n", "no\n", "\n", ""} {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			home, _ := withWorkspace(t)
			_, lib := libFlags(home)
			clock := withSessionGCSeams(t)
			ageSessions(t, lib, clock, "aged")
			withTerminalSeam(t, true)

			stdout, stderr, err := runCmdReader(t, strings.NewReader(answer), gcArgs(lib)...)
			if err != nil {
				t.Fatalf("answer %q: %v\n%s", answer, err, stderr)
			}
			if strings.Contains(stdout, "removed") {
				t.Fatalf("answer %q removed a session:\n%s", answer, stdout)
			}
			if !strings.Contains(stderr, "Type yes") {
				t.Fatalf("answer %q never saw the question: %q", answer, stderr)
			}
			if !sessionExists(home, "aged") {
				t.Fatalf("answer %q removed the session directory", answer)
			}
		})
	}
}

func TestSessionGCReportsAnEmptyPlanWithoutAsking(t *testing.T) {
	home, _ := withWorkspace(t)
	_, lib := libFlags(home)
	withSessionGCSeams(t)
	withTerminalSeam(t, true)

	stdout, stderr, err := runCmdReader(t,
		failReader{errors.New("an empty plan must not ask")}, gcArgs(lib)...)
	if err != nil {
		t.Fatalf("session gc: %v\n%s", err, stderr)
	}
	if !strings.Contains(stdout, "No session expired before") {
		t.Fatalf("stdout = %q, want the empty-plan title", stdout)
	}
	if strings.Contains(stdout+stderr, "Type yes") {
		t.Fatalf("the empty plan asked a question: %q %q", stdout, stderr)
	}
}

// TestSessionGCInitializesMissingLogsAndKeepsThemThisScan covers the
// one-time bootstrap at the command level: a record without an
// activity log is never a candidate of the initializing scan, not
// even with --expire 0, and the next scan decides from the recorded
// time without renewing it.
func TestSessionGCInitializesMissingLogsAndKeepsThemThisScan(t *testing.T) {
	home, _ := withWorkspace(t)
	_, lib := libFlags(home)
	clock := withSessionGCSeams(t)
	clock.age(90 * 24 * time.Hour)
	newGCSession(t, lib, "aged", false)
	clock.reset()
	seedLegacyRecord(t, home, "legacy")
	withTerminalSeam(t, false)

	stdout, stderr, _, err := runCmd(t, gcArgs(lib, "--expire", "0")...)
	if err != nil {
		t.Fatalf("first session gc: %v\n%s", err, stderr)
	}
	if !strings.Contains(stdout, "\n  aged ") || strings.Contains(stdout, "legacy") {
		t.Fatalf("first listing = %q, want the aged session only", stdout)
	}
	if !strings.Contains(stderr, "kept legacy: initialized") {
		t.Fatalf("stderr = %q, want the initialized skip", stderr)
	}
	if !sessionExists(home, "legacy") || !sessionExists(home, "aged") {
		t.Fatal("the preview run removed a session")
	}

	stdout, stderr, _, err = runCmd(t, gcArgs(lib, "--expire", "0")...)
	if err != nil {
		t.Fatalf("second session gc: %v\n%s", err, stderr)
	}
	if !strings.Contains(stdout, "\n  legacy ") {
		t.Fatalf("second listing = %q, want the legacy session listed", stdout)
	}
	if strings.Contains(stderr, "kept legacy") {
		t.Fatalf("the second run still keeps legacy: %q", stderr)
	}
}

// TestSessionGCReportsRemovedAndKeptOutcomes covers the partial
// execution: one session that changed and one that disappeared
// between the preview and the answer are reported kept with their
// reasons, the untouched one is removed, and the summary counts the
// actual outcome of the approved plan.
func TestSessionGCReportsRemovedAndKeptOutcomes(t *testing.T) {
	home, _ := withWorkspace(t)
	_, lib := libFlags(home)
	clock := withSessionGCSeams(t)
	ageSessions(t, lib, clock, "aged-a", "aged-b", "aged-c")
	withTerminalSeam(t, true)
	sessions := sessionsDir(home)
	reader := &confirmReader{answer: "yes\n", mutate: func() {
		w, err := activity.OpenLog(sessions, filepath.Join(sessions, "aged-b"),
			func() time.Time { return time.Now().UTC() })
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Append(activity.Event{Event: activity.EventSessionOpen,
			Phase: activity.PhaseSucceeded, Session: "aged-b"}); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(filepath.Join(sessions, "aged-c")); err != nil {
			t.Fatal(err)
		}
	}}

	stdout, stderr, err := runCmdReader(t, reader, gcArgs(lib)...)
	if err != nil {
		t.Fatalf("session gc: %v\n%s", err, stderr)
	}
	for _, want := range []string{
		"removed aged-a\n",
		"kept aged-b: changed (the activity log changed since the preview)",
		"kept aged-c: gone (the session is no longer stored)",
		"removed 1 of 3 approved sessions, kept 2\n",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("report missing %q:\n%s", want, stdout)
		}
	}
	if sessionExists(home, "aged-a") {
		t.Fatal("the unchanged session survived the confirmed gc")
	}
	if !sessionExists(home, "aged-b") {
		t.Fatal("the changed session was removed")
	}
}
