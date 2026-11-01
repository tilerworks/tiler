package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jiangxin/tiler/internal/config"
)

func TestListAndRemoveAbbreviations(t *testing.T) {
	withWorkspace(t)
	helps := [][]string{
		{"images", "--help"},
		{"images", "list", "--help"},
		{"images", "remove", "--help"},
		{"session", "--help"},
		{"session", "list", "--help"},
		{"session", "remove", "--help"},
	}
	for _, args := range helps {
		stdout, stderr, _, err := runCmd(t, args...)
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, stderr)
		}
		help := stdout + stderr
		if strings.Contains(help, "Aliases:") || strings.Contains(help, "\n  ls") || strings.Contains(help, "\n  rm") {
			t.Fatalf("%v help shows an abbreviation:\n%s", args, help)
		}
	}
	imagesHelp := mustHelp(t, "images", "--help")
	for _, want := range []string{"\n  list ", "\n  show ", "\n  remove ", "\n  gc "} {
		if !strings.Contains(imagesHelp, want) {
			t.Fatalf("images help missing %q:\n%s", want, imagesHelp)
		}
	}
	sessionHelp := mustHelp(t, "session", "--help")
	for _, want := range []string{"\n  list ", "\n  remove "} {
		if !strings.Contains(sessionHelp, want) {
			t.Fatalf("session help missing %q:\n%s", want, sessionHelp)
		}
	}
	for _, args := range [][]string{
		{"images", "list"},
		{"image", "list"},
		{"images", "ls"},
		{"session", "list"},
		{"session", "ls"},
	} {
		if _, stderr, _, err := runCmd(t, args...); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, stderr)
		}
	}
	for _, args := range [][]string{
		{"images", "remove"},
		{"images", "rm"},
		{"session", "remove"},
		{"session", "rm"},
	} {
		_, _, _, err := runCmd(t, args...)
		if ExitCode(err) != 2 {
			t.Fatalf("%v ExitCode(%v) = %d, want 2 (missing argument)", args, err, ExitCode(err))
		}
	}
}

func TestTopLevelAliasesUseCanonicalCommandsAndHelp(t *testing.T) {
	withWorkspace(t)
	rootHelp := mustHelp(t, "--help")
	for _, pair := range [][2]string{
		{"image", "images"},
		{"sessions", "session"},
		{"st", "status"},
		{"bench", "benchmark"},
	} {
		t.Run(pair[0], func(t *testing.T) {
			root := NewRootCmd()
			alias, _, err := root.Find([]string{pair[0]})
			if err != nil {
				t.Fatal(err)
			}
			canonical, _, err := root.Find([]string{pair[1]})
			if err != nil {
				t.Fatal(err)
			}
			if alias != canonical || alias.Name() != pair[1] {
				t.Fatalf("%s does not resolve to %s", pair[0], pair[1])
			}
			want := mustHelp(t, pair[1], "--help")
			for _, args := range [][]string{{pair[0], "--help"}, {"help", pair[0]}} {
				if got := mustHelp(t, args...); got != want {
					t.Fatalf("%v help differs from canonical help:\n%s", args, got)
				}
			}
			if strings.Contains(want, "Aliases:") || strings.Contains(rootHelp, "\n  "+pair[0]+" ") {
				t.Fatalf("help exposes alias %s", pair[0])
			}
		})
	}
}

func mustHelp(t *testing.T, args ...string) string {
	t.Helper()
	stdout, stderr, _, err := runCmd(t, args...)
	if err != nil {
		t.Fatalf("%v: %v\n%s", args, err, stderr)
	}
	return stdout + stderr
}

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
		{name: "git", version: "v0.1.0-2-gabcdef0-dirty", want: "tiler v0.1.0-2-gabcdef0-dirty\n"},
		{name: "archive", version: "0.1.0.change", want: "tiler 0.1.0.change\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			Version = tt.version
			for _, args := range [][]string{{"version"}, {"--version"}} {
				stdout, stderr, _, err := runCmd(t, args...)
				if err != nil {
					t.Fatal(err)
				}
				if stdout != tt.want || stderr != "" {
					t.Fatalf("%v: stdout = %q, stderr = %q, want %q", args, stdout, stderr, tt.want)
				}
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

	cfg := mustRun(t)
	if !cfg.CacheEnabled {
		t.Fatal("omitted --cache cleared cache_enabled from the file")
	}

	cfg = mustRun(t, "--cache=false")
	if cfg.CacheEnabled {
		t.Fatal("explicit --cache=false did not override the file")
	}

	writeProjectConfig(t, work, "cache_enabled: false\n")
	cfg = mustRun(t, "--cache")
	if !cfg.CacheEnabled {
		t.Fatal("explicit --cache did not override the file")
	}
}

func TestSessionAutocloseFlagOverlaysOnlyWhenExplicit(t *testing.T) {
	_, work := withWorkspace(t)
	writeProjectConfig(t, work, "session_autoclose_stale: true\n")

	cfg := mustRun(t)
	if !cfg.SessionAutocloseStale {
		t.Fatal("omitted flag cleared session_autoclose_stale from the file")
	}

	cfg = mustRun(t, "--session-autoclose-stale=false")
	if cfg.SessionAutocloseStale {
		t.Fatal("explicit --session-autoclose-stale=false did not override the file")
	}

	writeProjectConfig(t, work, "session_autoclose_stale: false\n")
	cfg = mustRun(t, "--session-autoclose-stale")
	if !cfg.SessionAutocloseStale {
		t.Fatal("explicit --session-autoclose-stale did not override the file")
	}
}

func TestSplitSizeFileLoads(t *testing.T) {
	_, work := withWorkspace(t)
	writeProjectConfig(t, work, "split_size: 2G\n")

	cfg := mustRun(t)
	if cfg.SplitSize != 2<<30 {
		t.Fatalf("split_size = %d, want 2G", cfg.SplitSize)
	}

	writeProjectConfig(t, work, "split_size: 0\n")
	cfg = mustRun(t)
	if cfg.SplitSize != 0 {
		t.Fatalf("explicit zero split_size = %d", cfg.SplitSize)
	}
}

func TestRemovedSplitFlagsAndKeys(t *testing.T) {
	_, work := withWorkspace(t)
	for _, args := range [][]string{
		{"--split"},
		{"--split-mode", "blocks"},
		{"--split-route", "concat"},
		{"version", "--split"},
	} {
		_, stderr, _, err := runCmd(t, args...)
		if ExitCode(err) != 2 {
			t.Fatalf("%v: ExitCode(%v) = %d, stderr %q", args, err, ExitCode(err), stderr)
		}
	}

	stdout, stderr, _, err := runCmd(t, "--help")
	if err != nil {
		t.Fatalf("help: %v\n%s", err, stderr)
	}
	help := stdout + stderr
	for _, flag := range []string{"--split", "--split-mode", "--split-route"} {
		if strings.Contains(help, flag) {
			t.Fatalf("help still documents %s:\n%s", flag, help)
		}
	}

	writeProjectConfig(t, work, "split_enabled: true\n")
	_, stderr, _, err = runCmd(t)
	if ExitCode(err) != 1 || !strings.Contains(stderr, "split_enabled") {
		t.Fatalf("project old key: ExitCode(%v) stderr %q", err, stderr)
	}
}

func TestRunCreatesUserConfigFile(t *testing.T) {
	home, _ := withWorkspace(t)
	path := filepath.Join(home, ".tiler.yaml")
	if _, err := os.Stat(path); err == nil {
		t.Fatal("user config already exists before any command")
	}
	if _, _, _, err := runCmd(t); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("user config not created: %v", err)
	}
	for _, want := range []string{
		"version: 1",
		"images_dir: .tiler/images",
		"sessions_dir: .tiler/sessions",
		"cache_dir: .tiler/cache",
		"logs_dir: .tiler/logs",
		"# split_size: 2G",
	} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("created config missing %q\n%s", want, data)
		}
	}
	if strings.Contains(string(data), "runtime_dir") {
		t.Fatalf("created config still mentions runtime_dir:\n%s", data)
	}
	if strings.Contains(string(data), "split_enabled") {
		t.Fatalf("created config still mentions split_enabled:\n%s", data)
	}
}

func TestRunKeepsExistingUserConfig(t *testing.T) {
	home, _ := withWorkspace(t)
	// A current-schema file must not be rewritten on later runs.
	writeProjectConfig(t, home, "version: 1\nlog_level: debug\n")
	if _, _, _, err := runCmd(t); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".tiler.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "version: 1\nlog_level: debug\n" {
		t.Fatalf("existing user config was rewritten:\n%s", data)
	}
}

func TestRunUpgradesLegacyUserConfig(t *testing.T) {
	home, _ := withWorkspace(t)
	path := filepath.Join(home, ".tiler.yaml")
	legacy := "runtime_dir: ~/.tiler/runtime\nlog_level: debug\n"
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := runCmd(t); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.Contains(got, "version: 1") {
		t.Fatalf("legacy config was not upgraded:\n%s", got)
	}
	if strings.Contains(got, "runtime_dir") {
		t.Fatalf("upgraded config still has runtime_dir:\n%s", got)
	}
	backup, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	if string(backup) != legacy {
		t.Fatalf("backup = %q, want the original bytes", backup)
	}
}

func TestRunUpgradeFailurePrefersUpgradeError(t *testing.T) {
	home, _ := withWorkspace(t)
	path := filepath.Join(home, ".tiler.yaml")
	legacy := "runtime_dir: ~/.tiler/runtime\nlog_level: debug\n"
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	// Read-only home blocks the backup/rewrite temp files, so upgrade
	// fails while Load would only report an opaque unknown-field error.
	if err := os.Chmod(home, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(home, 0o755) })

	_, stderr, _, err := runCmd(t)
	if ExitCode(err) == 0 {
		t.Fatal("expected failure when upgrade cannot rewrite a legacy file")
	}
	combined := stderr
	if err != nil {
		combined += "\n" + err.Error()
	}
	if !strings.Contains(strings.ToLower(combined), "upgrade") {
		t.Fatalf("error = %q, want an upgrade mention", combined)
	}
	// Must not surface solely as the strict Decode unknown-field path.
	if strings.Contains(combined, "field") && !strings.Contains(strings.ToLower(combined), "upgrade") {
		t.Fatalf("surfaced as unknown field without upgrade: %q", combined)
	}
}

func TestCeilingDirBoundsProjectWalk(t *testing.T) {
	home, _ := withWorkspace(t)
	// A marker project file above the ceiling sets a distinctive dir.
	zone := filepath.Join(home, "zone")
	sandbox := filepath.Join(zone, "sandbox")
	deep := filepath.Join(sandbox, "deep")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	writeProjectConfig(t, zone, "cache_dir: marker-cache\n")
	t.Chdir(deep)

	// Without a ceiling the walk reaches zone and applies the marker.
	cfg := mustRun(t)
	if cfg.CacheDir != filepath.Join(zone, "marker-cache") {
		t.Fatalf("control cache_dir = %q, want the zone marker", cfg.CacheDir)
	}

	// With the ceiling at sandbox, zone sits above it and is excluded, so
	// the default cache dir stands.
	t.Setenv("TILER_CEILING_DIR", sandbox)
	cfg = mustRun(t)
	if cfg.CacheDir != filepath.Join(home, ".tiler", "cache") {
		t.Fatalf("ceiling cache_dir = %q, want the default", cfg.CacheDir)
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
