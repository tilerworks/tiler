package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOutsideReserved(t *testing.T) {
	home := t.TempDir()
	data := t.TempDir()
	images := filepath.Join(data, "images")
	sessions := filepath.Join(data, "sessions")
	cache := filepath.Join(data, "cache")
	cfg, err := Load(home, home, home, "", Patch{
		ImagesDir:   &images,
		SessionsDir: &sessions,
		CacheDir:    &cache,
	})
	if err != nil {
		t.Fatal(err)
	}

	layout := filepath.Join(home, ".tiler")
	cases := []struct {
		name   string
		path   string
		reject bool
	}{
		{name: "home layout root", path: layout, reject: true},
		{name: "home layout descendant", path: filepath.Join(layout, "nested", "ws"), reject: true},
		{name: "images dir", path: images, reject: true},
		{name: "images descendant", path: filepath.Join(images, "child"), reject: true},
		{name: "session runtime descendant", path: filepath.Join(sessions, "dev", "runtime", "m1"), reject: true},
		{name: "sessions dir", path: sessions, reject: true},
		{name: "cache descendant", path: filepath.Join(cache, "blob"), reject: true},
		{name: "workspace outside", path: filepath.Join(data, "work"), reject: false},
		{name: "missing path outside", path: filepath.Join(data, "missing", "ws"), reject: false},
		{name: "prefix neighbor of home layout", path: layout + "-backup", reject: false},
		{name: "project under home", path: filepath.Join(home, "src", "app"), reject: false},
		// An ancestor of a reserved root is not "inside" it; the
		// cover check owns that refusal.
		{name: "ancestor of sessions", path: data, reject: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := OutsideReserved(tc.path, cfg)
			if tc.reject && err == nil {
				t.Fatal("path was accepted")
			}
			if !tc.reject && err != nil {
				t.Fatal(err)
			}
			if tc.reject && !errors.Is(err, ErrReservedPath) {
				t.Fatalf("err = %v, want ErrReservedPath", err)
			}
		})
	}
}

func TestCoversReserved(t *testing.T) {
	home := t.TempDir()
	data := t.TempDir()
	images := filepath.Join(data, "images")
	sessions := filepath.Join(data, "sessions")
	cache := filepath.Join(data, "cache")
	cfg, err := Load(home, home, home, "", Patch{
		ImagesDir:   &images,
		SessionsDir: &sessions,
		CacheDir:    &cache,
	})
	if err != nil {
		t.Fatal(err)
	}

	layout := filepath.Join(home, ".tiler")
	cases := []struct {
		name   string
		path   string
		reject bool
		want   string
	}{
		{name: "ancestor of sessions", path: data, reject: true, want: "would cover reserved"},
		{name: "ancestor of home layout", path: home, reject: true, want: "would cover reserved"},
		{name: "sessions equality left to OutsideReserved", path: sessions, reject: false},
		{name: "sessions descendant", path: filepath.Join(sessions, "dev"), reject: false},
		{name: "workspace outside", path: filepath.Join(data, "work"), reject: false},
		{name: "prefix neighbor", path: layout + "-backup", reject: false},
		{name: "images sibling workspace", path: filepath.Join(data, "work"), reject: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CoversReserved(tc.path, cfg)
			if tc.reject && err == nil {
				t.Fatal("path was accepted")
			}
			if !tc.reject && err != nil {
				t.Fatal(err)
			}
			if tc.reject && !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want substring %q", err, tc.want)
			}
			if tc.reject && !errors.Is(err, ErrReservedPath) {
				t.Fatalf("err = %v, want ErrReservedPath", err)
			}
		})
	}
}

func TestOutsideReservedSymlinkIntoImages(t *testing.T) {
	home := t.TempDir()
	data := t.TempDir()
	images := filepath.Join(data, "images")
	sessions := filepath.Join(data, "sessions")
	cache := filepath.Join(data, "cache")
	if err := os.MkdirAll(images, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(home, home, home, "", Patch{
		ImagesDir:   &images,
		SessionsDir: &sessions,
		CacheDir:    &cache,
	})
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(data, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(work, "via-images")
	if err := os.Symlink(images, link); err != nil {
		t.Fatal(err)
	}
	err = OutsideReserved(link, cfg)
	if err == nil || !errors.Is(err, ErrReservedPath) {
		t.Fatalf("err = %v, want ErrReservedPath", err)
	}
	if !strings.Contains(err.Error(), "inside reserved") {
		t.Fatalf("err = %v", err)
	}
}

func TestReservedPathErrorsQuoteSpaces(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "trash directory.case")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(home, home, home, "", Patch{})
	if err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(cfg.ImagesDir, "alice", "app")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	refuse := OutsideReserved(inside, cfg)
	if refuse == nil {
		t.Fatal("path was accepted")
	}
	resolvedInside, err := resolveComparePath(inside)
	if err != nil {
		t.Fatal(err)
	}
	resolvedLayout, err := resolveComparePath(filepath.Join(home, ".tiler"))
	if err != nil {
		t.Fatal(err)
	}
	text := refuse.Error()
	for _, path := range []string{resolvedInside, resolvedLayout} {
		quoted := fmt.Sprintf("%q", path)
		if !strings.Contains(text, quoted) {
			t.Fatalf("err = %q, want quoted path %s", text, quoted)
		}
	}
}

func TestCoversReservedSymlinkToAncestor(t *testing.T) {
	home := t.TempDir()
	data := t.TempDir()
	images := filepath.Join(data, "images")
	sessions := filepath.Join(data, "sessions")
	cache := filepath.Join(data, "cache")
	if err := os.MkdirAll(images, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(home, home, home, "", Patch{
		ImagesDir:   &images,
		SessionsDir: &sessions,
		CacheDir:    &cache,
	})
	if err != nil {
		t.Fatal(err)
	}
	aliasRoot := t.TempDir()
	link := filepath.Join(aliasRoot, "cover")
	if err := os.Symlink(data, link); err != nil {
		t.Fatal(err)
	}
	err = CoversReserved(link, cfg)
	if err == nil || !errors.Is(err, ErrReservedPath) {
		t.Fatalf("err = %v, want ErrReservedPath", err)
	}
	if !strings.Contains(err.Error(), "would cover reserved") {
		t.Fatalf("err = %v", err)
	}
}
