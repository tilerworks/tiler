package cli

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/mountinfo"
	"github.com/jiangxin/tiler/internal/session"
)

func TestSessionDetailsShareStatusAndVerboseAliases(t *testing.T) {
	home, _ := withWorkspace(t)
	lib, _ := withStatusSeeds(t, home, map[string]bool{"/ws/dev": true})
	t.Setenv(sessionEnvVar, "dev")
	previous := sessionTimeNow
	sessionTimeNow = func() time.Time { return goldenSessionTime }
	t.Cleanup(func() { sessionTimeNow = previous })
	for _, name := range []string{"dev", "old"} {
		status, _, _, err := runCmd(t, append(lib, "status", "-v", "-s", name)...)
		if err != nil {
			t.Fatal(err)
		}
		shown, stderr, _, err := runCmd(t, append(lib, "session", "show", name)...)
		if err != nil || shown != status {
			t.Fatalf("show %s: %v %s\n%s\nwant:\n%s", name, err, stderr, shown, status)
		}
	}
	var expected string
	for _, args := range [][]string{{"session", "list", "-v"}, {"session", "ls", "--verbose"}, {"session", "-v"}} {
		out, stderr, _, err := runCmd(t, append(lib, args...)...)
		if err != nil || !strings.Contains(out, "dev · active · current") || !strings.Contains(out, "old · closed") || strings.Contains(out, "orphan ") {
			t.Fatalf("verbose %v: %v %s\n%s", args, err, stderr, out)
		}
		if len(args) == 2 {
			_, out, _ = strings.Cut(out, "\n\n")
		}
		if expected != "" && out != expected {
			t.Fatalf("verbose aliases differ: %s", out)
		}
		expected = out
	}
}

func TestSessionJSONAndBriefListAvoidHostQueries(t *testing.T) {
	home, _ := withWorkspace(t)
	_, lib := libFlags(home)
	writeGoldenSessionRecords(t, home)
	withSessionSeams(t, func(e *session.Engine) {
		e.LoadTable = func() ([]mountinfo.Mount, error) { return nil, errors.New("host query failed") }
	})
	brief, stderr, _, err := runCmd(t, append(lib, "session", "list")...)
	if err != nil || !strings.Contains(brief, "LAST ACTIVITY") {
		t.Fatalf("brief: %v %s %s", err, stderr, brief)
	}
	plain, _, _, err := runCmd(t, append(lib, "session", "list", "--json")...)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"session", "list", "-v", "--json"}, {"session", "-v", "--json"}} {
		out, stderr, _, err := runCmd(t, append(lib, args...)...)
		if err != nil || out != plain {
			t.Fatalf("verbose JSON: %v %s %s", err, stderr, out)
		}
	}
	if _, stderr, _, err := runCmd(t, append(lib, "session", "show", "dev", "--json")...); err != nil {
		t.Fatalf("show JSON: %v %s", err, stderr)
	}
	for _, args := range [][]string{{"session", "list", "-v"}, {"session", "show", "dev"}} {
		if _, _, _, err := runCmd(t, append(lib, args...)...); err == nil || !strings.Contains(err.Error(), "host query failed") {
			t.Fatalf("host error = %v", err)
		}
	}
}

func TestSessionDetailsHandleEmptyLockedAndInvalidNames(t *testing.T) {
	home, _ := withWorkspace(t)
	_, lib := libFlags(home)
	writeSessionRecord(t, sessionsDir(home), "empty")
	withSessionSeams(t, func(e *session.Engine) {
		e.LoadTable = func() ([]mountinfo.Mount, error) { return nil, nil }
		e.LoadDiskImages = nil
	})
	out, _, _, err := runCmd(t, append(lib, "session", "show", "empty")...)
	if err != nil || !strings.Contains(out, "empty · active") || strings.Contains(out, "📁") {
		t.Fatalf("empty: %v %s", err, out)
	}
	withSessionSeams(t, func(e *session.Engine) {
		e.Lock = func(string, time.Duration) (func(), error) { return nil, session.ErrLockHeld }
	})
	out, _, _, err = runCmd(t, append(lib, "session", "show", "empty")...)
	if err != nil || out != "empty · locked · activity-unknown\n" {
		t.Fatalf("locked: %v %s", err, out)
	}
	for _, tc := range []struct {
		name string
		code int
	}{{"bad name", 2}, {"missing", 1}} {
		_, _, _, err := runCmd(t, append(lib, "session", "show", tc.name)...)
		if ExitCode(err) != tc.code {
			t.Fatalf("%q: exit %d (%v)", tc.name, ExitCode(err), err)
		}
	}
}
