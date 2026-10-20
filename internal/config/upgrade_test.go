package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpgradeUserFileRewritesLegacy(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, fileName)
	legacy := strings.Join([]string{
		"images_dir: ~/.tiler/images",
		"runtime_dir: ~/.tiler/runtime",
		"sessions_dir: ~/.tiler/sessions",
		"cache_dir: ~/.tiler/cache",
		"log_level: debug",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := UpgradeUserFile(home); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.Contains(got, "version: 1") {
		t.Fatalf("upgraded file missing version: 1\n%s", got)
	}
	if strings.Contains(got, "runtime_dir") {
		t.Fatalf("upgraded file still has runtime_dir:\n%s", got)
	}
	if !strings.Contains(got, "log_level: debug") {
		t.Fatalf("upgraded file dropped log_level:\n%s", got)
	}

	backup, err := os.ReadFile(path + backupSuffix)
	if err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	if string(backup) != legacy {
		t.Fatalf("backup = %q, want the original bytes", backup)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("perm = %o, want 600", perm)
	}

	// The upgraded file must decode under the strict KnownFields path.
	if _, err := Decode(data); err != nil {
		t.Fatalf("Decode after upgrade: %v", err)
	}
}

func TestUpgradeUserFileStripsSplitKeys(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, fileName)
	legacy := strings.Join([]string{
		"version: 1",
		"split_enabled: true",
		"split_mode: blocks",
		"split_route: concat",
		"log_level: debug",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := UpgradeUserFile(home); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, key := range []string{"split_enabled", "split_mode", "split_route"} {
		if strings.Contains(got, key) {
			t.Fatalf("upgraded file still has %s:\n%s", key, got)
		}
	}
	if !strings.Contains(got, "log_level: debug") {
		t.Fatalf("upgraded file dropped log_level:\n%s", got)
	}
	cfg, err := Load(home, home, home, "", Patch{})
	if err != nil {
		t.Fatalf("Load after upgrade: %v", err)
	}
	if cfg.SplitSize != 0 || cfg.LogLevel != "debug" {
		t.Fatalf("loaded = %+v", cfg)
	}
}

func TestUpgradeUserFileSkipsCurrent(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, fileName)
	current := "version: 1\nlog_level: warn\n"
	if err := os.WriteFile(path, []byte(current), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := UpgradeUserFile(home); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != current {
		t.Fatalf("compatible file was rewritten:\n%s", got)
	}
	if _, err := os.Stat(path + backupSuffix); !os.IsNotExist(err) {
		t.Fatalf("backup created for a compatible file: %v", err)
	}
}

func TestUpgradeUserFileMissingIsNoop(t *testing.T) {
	home := t.TempDir()
	if err := UpgradeUserFile(home); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, fileName)); !os.IsNotExist(err) {
		t.Fatalf("missing file was created: %v", err)
	}
}

func TestUpgradeUserFileReadOnlyDir(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, fileName)
	legacy := "runtime_dir: ~/.tiler/runtime\nlog_level: info\n"
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(home, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(home, 0o755) })

	err := UpgradeUserFile(home)
	if err == nil {
		t.Fatal("expected an error on a read-only home")
	}
	// The original file must still be readable and unchanged.
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != legacy {
		t.Fatalf("read-only failure mutated the file:\n%s", got)
	}
}

func TestUpgradeUserFileRewritesMissingVersion(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, fileName)
	legacy := "log_level: debug\n"
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := UpgradeUserFile(home); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "version: 1") {
		t.Fatalf("missing version was not added:\n%s", got)
	}
	if _, err := os.Stat(path + backupSuffix); err != nil {
		t.Fatalf("backup missing: %v", err)
	}
}

func TestUpgradeUserFileRewritesFloatVersion(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, fileName)
	// YAML 1.0 is a float scalar; Decode rejects it, so upgrade must
	// rewrite rather than treating the file as already current.
	legacy := "version: 1.0\nlog_level: debug\n"
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := UpgradeUserFile(home); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) == legacy {
		t.Fatal("float version scalar was left unchanged")
	}
	if !strings.Contains(string(got), "version: 1\n") {
		t.Fatalf("upgraded file missing integer version: 1\n%s", got)
	}
	if strings.Contains(string(got), "1.0") {
		t.Fatalf("float version scalar survived rewrite:\n%s", got)
	}
	if _, err := Decode(got); err != nil {
		t.Fatalf("Decode after float-version rewrite: %v", err)
	}
	backup, err := os.ReadFile(path + backupSuffix)
	if err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	if string(backup) != legacy {
		t.Fatalf("backup = %q, want the original bytes", backup)
	}
}
