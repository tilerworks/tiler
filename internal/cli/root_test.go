package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jiangxin/tiler/internal/config"
)

func TestVersionLineUsesDefaultAndOverride(t *testing.T) {
	withWorkspace(t)
	original := Version
	t.Cleanup(func() { Version = original })

	tests := []struct {
		name    string
		version string
		want    string
	}{
		{name: "default", version: "dev", want: "tiler dev\n"},
		{name: "overridden", version: "1.2.3", want: "tiler 1.2.3\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			Version = tt.version
			stdout, _, _, err := runCmd(t, "version")
			if err != nil {
				t.Fatal(err)
			}
			if ExitCode(err) != 0 {
				t.Fatalf("ExitCode = %d, want 0", ExitCode(err))
			}
			if stdout != tt.want {
				t.Fatalf("stdout = %q, want %q", stdout, tt.want)
			}
		})
	}
}

func TestUsageErrorsExitWithCode2(t *testing.T) {
	withWorkspace(t)
	tests := []struct {
		name string
		args []string
	}{
		{name: "unknown command", args: []string{"missing"}},
		{name: "bad flag", args: []string{"--not-a-flag"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, stderr, _, err := runCmd(t, tt.args...)
			if ExitCode(err) != 2 {
				t.Fatalf("ExitCode(%v) = %d, want 2", err, ExitCode(err))
			}
			if tt.name == "bad flag" && !strings.Contains(stdout+stderr, "Usage:") {
				t.Fatalf("flag error did not print usage\nstdout: %s\nstderr: %s", stdout, stderr)
			}
		})
	}
}

func TestWorkingDirectoryInsideReservedTreeExitsWithCode1(t *testing.T) {
	home := t.TempDir()
	inside := filepath.Join(home, ".tiler", "images")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Chdir(inside)

	stdout, stderr, _, err := runCmd(t)
	if ExitCode(err) != 1 {
		t.Fatalf("ExitCode(%v) = %d, want 1", err, ExitCode(err))
	}
	if strings.Contains(stdout+stderr, "Usage:") {
		t.Fatalf("runtime failure printed usage\nstdout: %s\nstderr: %s", stdout, stderr)
	}
}

func TestCacheFlagOverlaysOnlyWhenExplicit(t *testing.T) {
	_, work := withWorkspace(t)
	writeProjectConfig(t, work, "cache_enabled: true\n")

	cfg := mustRun(t, "version")
	if !cfg.CacheEnabled {
		t.Fatal("omitted --cache cleared cache_enabled from the file")
	}

	cfg = mustRun(t, "version", "--cache=false")
	if cfg.CacheEnabled {
		t.Fatal("explicit --cache=false did not override the file")
	}

	writeProjectConfig(t, work, "cache_enabled: false\n")
	cfg = mustRun(t, "version", "--cache")
	if !cfg.CacheEnabled {
		t.Fatal("explicit --cache did not override the file")
	}
}

func TestSessionAutocloseFlagOverlaysOnlyWhenExplicit(t *testing.T) {
	_, work := withWorkspace(t)
	writeProjectConfig(t, work, "session_autoclose_stale: true\n")

	cfg := mustRun(t, "version")
	if !cfg.SessionAutocloseStale {
		t.Fatal("omitted flag cleared session_autoclose_stale from the file")
	}

	cfg = mustRun(t, "version", "--session-autoclose-stale=false")
	if cfg.SessionAutocloseStale {
		t.Fatal("explicit --session-autoclose-stale=false did not override the file")
	}

	writeProjectConfig(t, work, "session_autoclose_stale: false\n")
	cfg = mustRun(t, "version", "--session-autoclose-stale")
	if !cfg.SessionAutocloseStale {
		t.Fatal("explicit --session-autoclose-stale did not override the file")
	}
}

func withWorkspace(t *testing.T) (home, work string) {
	t.Helper()
	home = t.TempDir()
	work = filepath.Join(home, "work")
	if err := os.Mkdir(work, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Chdir(work)
	return home, work
}

func writeProjectConfig(t *testing.T, dir, body string) {
	t.Helper()
	path := filepath.Join(dir, ".tiler.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustRun(t *testing.T, args ...string) config.Config {
	t.Helper()
	_, stderr, cfg, err := runCmd(t, args...)
	if err != nil {
		t.Fatalf("command %v: %v\n%s", args, err, stderr)
	}
	return cfg
}

func runCmd(t *testing.T, args ...string) (stdout, stderr string, cfg config.Config, err error) {
	t.Helper()
	cmd := NewRootCmd()
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	cmd.SetArgs(args)
	err = cmd.Execute()
	cfg, _ = loadedConfig(cmd)
	return out.String(), errBuf.String(), cfg, err
}
