package config

import (
	"path/filepath"
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
		})
	}
}
