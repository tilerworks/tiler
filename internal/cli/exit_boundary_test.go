package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/jiangxin/tiler/internal/config"
	"github.com/jiangxin/tiler/internal/doctor"
	"github.com/jiangxin/tiler/internal/mountinfo"
	"github.com/jiangxin/tiler/internal/session"
)

// TestExitCodeBoundaries locks the unified exit-code contract across
// the command tree: success is 0, a usage problem — an unknown
// command, a flag parse failure, a wrong argument count, or an
// invalid name — is 2, and a runtime or probe failure is 1.
func TestExitCodeBoundaries(t *testing.T) {
	tests := []struct {
		name string
		seed func(t *testing.T, home string)
		args []string
		want int
	}{
		{name: "version succeeds", args: []string{"version"}, want: 0},
		{name: "empty library lists clean", args: []string{"images", "ls"}, want: 0},
		{name: "empty session list succeeds", args: []string{"session", "list"}, want: 0},
		{name: "status without records succeeds",
			seed: seedEmptyStatusSeams, args: []string{"status"}, want: 0},
		{name: "unknown command is usage", args: []string{"missing"}, want: 2},
		{name: "unknown flag is usage", args: []string{"--not-a-flag"}, want: 2},
		{name: "status with two paths is usage",
			args: []string{"status", "a", "b"}, want: 2},
		{name: "session show without a name is usage",
			args: []string{"session", "show"}, want: 2},
		{name: "mount with one argument is usage",
			args: []string{"mount", "only"}, want: 2},
		{name: "invalid session name is usage",
			args: []string{"session", "show", "bad name"}, want: 2},
		{name: "invalid image name is usage",
			args: []string{"images", "show", "_foo"}, want: 2},
		{name: "missing library image is runtime",
			args: []string{"images", "show", "nope"}, want: 1},
		{name: "images list with a name is usage",
			args: []string{"images", "ls", "alice/app"}, want: 2},
		{name: "doctor with a failing probe is runtime",
			seed: seedFailingDoctor, args: []string{"doctor"}, want: 1},
		{name: "uncovered status path is runtime",
			seed: seedEmptyStatusSeams, args: []string{"status", "/nowhere"}, want: 1},
		{name: "missing session record is runtime",
			args: []string{"session", "show", "ghost"}, want: 1},
		{name: "reserved tree working directory is runtime",
			seed: seedReservedCwd, args: []string{"version"}, want: 1},
		{name: "corrupt block manifest mount is runtime",
			seed: seedCorruptBlocks,
			args: []string{"mount", "alice/data", "/ws/view"}, want: 1},
		{name: "removed split flag is usage",
			args: []string{"--split", "version"}, want: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home, _ := withWorkspace(t)
			if tt.seed != nil {
				tt.seed(t, home)
			}
			_, _, _, err := runCmd(t, tt.args...)
			if ExitCode(err) != tt.want {
				t.Fatalf("ExitCode(%v) = %d, want %d", err, ExitCode(err), tt.want)
			}
		})
	}
}

// seedEmptyStatusSeams keeps the status command off the host mount
// table and health probes.
func seedEmptyStatusSeams(t *testing.T, _ string) {
	t.Helper()
	withSessionSeams(t, func(engine *session.Engine) {
		engine.LoadTable = func() ([]mountinfo.Mount, error) { return nil, nil }
		engine.Health = func(string) bool { return false }
	})
}

// seedFailingDoctor replaces the probe set with one failing result.
func seedFailingDoctor(t *testing.T, _ string) {
	t.Helper()
	restoreDoctor(t, func(context.Context, config.Config, io.Writer) ([]doctor.Result, error) {
		return []doctor.Result{{Name: "git", Status: "fail", Detail: "missing"}}, nil
	})
}

// seedReservedCwd moves the working directory inside the reserved
// runtime tree, which startup refuses.
func seedReservedCwd(t *testing.T, home string) {
	t.Helper()
	inside := filepath.Join(home, ".tiler", "images")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(inside)
}

// defaultImagesDir is the library root of the exit-boundary tests,
// which run without the library flags.
func defaultImagesDir(home string) string {
	return filepath.Join(home, ".tiler", "images")
}

// seedCorruptBlocks installs the mount seams and a blocks-form
// library whose first block disagrees with its recorded size.
func seedCorruptBlocks(t *testing.T, home string) {
	t.Helper()
	withMountSeams(t, nil)
	imagesDir := defaultImagesDir(home)
	id := writeBlocksFixture(t, imagesDir, "alice/data", []string{"one", "two"}, true)
	writeSplitFile(t,
		filepath.Join(imagesDir, "alice", "data", "_blocks", id, "blk.000"),
		"corrupted-and-longer")
}
