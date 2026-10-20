package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureUserFileCreatesTemplate(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, fileName)
	if _, err := os.Stat(path); err == nil {
		t.Fatal("user file already exists before EnsureUserFile")
	}
	if err := EnsureUserFile(home); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"version: 1",
		"images_dir: ~/.tiler/images",
		"sessions_dir: ~/.tiler/sessions",
		"cache_dir: ~/.tiler/cache",
		"log_level: info",
		"merged root",
		"# image_format: squashfs",
		"# image_compression: zstd",
		"# split_size: 2G",
	} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("template missing %q\n%s", want, data)
		}
	}
	if strings.Contains(string(data), "runtime_dir") {
		t.Fatalf("template still mentions runtime_dir:\n%s", data)
	}
	for _, banned := range []string{"split_enabled", "split_mode", "split_route"} {
		if strings.Contains(string(data), banned) {
			t.Fatalf("template still mentions %s:\n%s", banned, data)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("perm = %o, want 600", perm)
	}
	// The created file must decode and load cleanly, resolving to the
	// same defaults the template spells out.
	cfg, err := Load(home, home, home, "", Patch{})
	if err != nil {
		t.Fatalf("load with created file: %v", err)
	}
	if cfg.ImagesDir != filepath.Join(home, ".tiler", "images") {
		t.Fatalf("images = %q after loading the created file", cfg.ImagesDir)
	}
	if cfg.SplitSize != 0 {
		t.Fatalf("split size = %d, want the commented example to stay off", cfg.SplitSize)
	}
	if cfg.ImageFormat != "" || cfg.ImageCompression != "" {
		t.Fatalf("commented container keys were applied: %s/%s",
			cfg.ImageFormat, cfg.ImageCompression)
	}
	if cfg.UserFile() != path {
		t.Fatalf("UserFile() = %q, want %q", cfg.UserFile(), path)
	}
}

func TestEnsureUserFileKeepsExisting(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, fileName)
	want := "log_level: debug\n"
	if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureUserFile(home); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("existing file was rewritten:\n%s", got)
	}
}

func TestEnsureUserFileLeavesNoTempFiles(t *testing.T) {
	home := t.TempDir()
	if err := EnsureUserFile(home); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != fileName {
		names := make([]string, len(entries))
		for i, entry := range entries {
			names[i] = entry.Name()
		}
		t.Fatalf("home has unexpected entries: %v", names)
	}
}
