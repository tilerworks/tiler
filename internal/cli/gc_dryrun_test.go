package cli

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGCDryRunPreservesCandidatesWithoutReadingInput(t *testing.T) {
	for _, command := range []string{"images", "image", "session", "sessions"} {
		for _, flag := range []string{"-n", "--dry-run"} {
			for _, terminal := range []bool{false, true} {
				t.Run(command+flag+map[bool]string{false: "pipe", true: "terminal"}[terminal], func(t *testing.T) {
					home, lib, repo, nodes := imageGCFixture(t)
					clock := withSessionGCSeams(t)
					if command == "session" || command == "sessions" {
						ageSessions(t, lib, clock, "aged")
					}
					runtimeFile := filepath.Join(sessionsDir(home), "aged", "runtime", "m1", "upper", "work")
					if command == "session" || command == "sessions" {
						writeTestFile(t, runtimeFile, "keep")
					}
					withTerminalSeam(t, terminal)
					args := append(append([]string{}, lib...), command, "gc", flag)
					if !terminal {
						args = append(args, "--yes")
					}
					out, stderr, err := runCmdReader(t, failReader{errors.New("must not read")}, args...)
					if err != nil || !strings.Contains(out, "Dry run: nothing removed.") || strings.Contains(out, "approved") || strings.Contains(stderr, "Type yes") || strings.Contains(stderr, "rerun with --yes") {
						t.Fatalf("output=%q stderr=%q error=%v", out, stderr, err)
					}
					if command == "images" || command == "image" {
						for _, node := range nodes {
							if !strings.Contains(out, "expired  alice/app:"+node.ID) {
								t.Fatalf("missing candidate: %s", out)
							}
						}
					} else if !strings.Contains(out, "  aged ") {
						t.Fatalf("missing session: %s", out)
					}
					assertImageGCObjects(t, repo, nodes, true)
					if body, err := os.ReadFile(runtimeFile); (command == "session" || command == "sessions") && (err != nil || string(body) != "keep") {
						t.Fatalf("runtime changed: %q %v", body, err)
					}
				})
			}
		}
	}
}

func TestConfirmationDryRunEmptyPlanAndWriteFailures(t *testing.T) {
	failure := errors.New("stream failed")
	for _, empty := range []bool{false, true} {
		for _, failAt := range []int{0, 1, 4} {
			out := &dryRunWriter{failAt: failAt, err: failure}
			c := testConfirmation(t, failReader{errors.New("must not read")}, out, io.Discard)
			c.DryRun, c.AssumeYes = true, true
			if empty {
				c.Preview.Rows = nil
				out.failAt = min(failAt, 2)
			}
			approved, err := c.approved()
			if approved || (failAt != 0 && !errors.Is(err, failure)) || (failAt == 0 && err != nil) {
				t.Fatalf("empty=%v failAt=%d approved=%v error=%v", empty, failAt, approved, err)
			}
		}
	}
}

type dryRunWriter struct {
	writes, failAt int
	err            error
}

func (w *dryRunWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes == w.failAt {
		return 0, w.err
	}
	return len(p), nil
}

func TestSessionGCDryRunInitializesMissingActivity(t *testing.T) {
	home, _ := withWorkspace(t)
	_, lib := libFlags(home)
	withSessionGCSeams(t)
	seedLegacyRecord(t, home, "legacy")
	out, stderr, err := runCmdReader(t, failReader{errors.New("must not read")}, gcArgs(lib, "-n", "--yes", "--expire=0")...)
	if err != nil || !strings.Contains(stderr, "kept legacy: initialized") || !strings.Contains(out, "Dry run: nothing removed.") || !sessionExists(home, "legacy") {
		t.Fatalf("out=%q stderr=%q error=%v", out, stderr, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".tiler", "logs", "sessions", "legacy.log")); err != nil {
		t.Fatal(err)
	}
}

func TestImagesGCDryRunInitializesMissingActivity(t *testing.T) {
	home, lib, repo, nodes := imageGCFixture(t)
	log := filepath.Join(home, ".tiler", "logs", "images", "alice", "app", nodes[0].ID+".log")
	if err := os.Remove(log); err != nil {
		t.Fatal(err)
	}
	out, stderr, err := runCmdReader(t, failReader{errors.New("must not read")}, imageGCArgs(lib, "-n", "--yes", "--expire=0")...)
	if err != nil || !strings.Contains(stderr, "initialized") || strings.Contains(out, "expired  alice/app:"+nodes[0].ID) || !strings.Contains(out, "Dry run: nothing removed.") {
		t.Fatalf("out=%q stderr=%q error=%v", out, stderr, err)
	}
	assertImageGCObjects(t, repo, nodes, true)
	if _, err := os.Stat(log); err != nil {
		t.Fatal(err)
	}
}
