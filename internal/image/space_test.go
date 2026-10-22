package image

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func stubSpace(t *testing.T, avail int64, devOf func(string) uint64) {
	t.Helper()
	prev := probeSpace
	t.Cleanup(func() { probeSpace = prev })
	probeSpace = func(path string) (spaceInfo, error) {
		dev := uint64(1)
		if devOf != nil {
			dev = devOf(path)
		}
		return spaceInfo{avail: avail, dev: dev}, nil
	}
}

func TestEstimateTreeCountsFilesAndMargin(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a"), []byte("abcd"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "b"), []byte("xy"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	got, err := estimateTree(root)
	if err != nil {
		t.Fatal(err)
	}
	linkLen := int64(len("a"))
	if got != 4+2+linkLen+spaceMargin {
		t.Fatalf("estimate = %d, want file bytes plus margin", got)
	}
}

func TestEnsureSpaceRefusesWhenShort(t *testing.T) {
	stubSpace(t, 10, nil)
	err := ensureSpace(spaceWrite{path: t.TempDir(), bytes: 100})
	if err == nil || !strings.Contains(err.Error(), "not enough free space") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "need 100 bytes, 10 available") {
		t.Fatalf("err = %v", err)
	}
}

func TestEnsureSpaceAddsWritesOnOneVolume(t *testing.T) {
	stubSpace(t, 100, nil)
	err := ensureSpace(
		spaceWrite{path: "/cache", bytes: 60},
		spaceWrite{path: "/images", bytes: 60},
	)
	if err == nil || !strings.Contains(err.Error(), "need 120 bytes, 100 available") {
		t.Fatalf("err = %v", err)
	}
}

func TestEnsureSpaceAllowsSeparateVolumes(t *testing.T) {
	stubSpace(t, 80, func(path string) uint64 {
		if path == "/cache" {
			return 1
		}
		return 2
	})
	if err := ensureSpace(
		spaceWrite{path: "/cache", bytes: 60},
		spaceWrite{path: "/images", bytes: 60},
	); err != nil {
		t.Fatal(err)
	}
}

func TestProbeSpaceOSSeesTempDir(t *testing.T) {
	info, err := probeSpaceOS(filepath.Join(t.TempDir(), "missing", "repo"))
	if err != nil {
		t.Fatal(err)
	}
	if info.avail <= 0 {
		t.Fatalf("avail = %d", info.avail)
	}
}

func TestEstimateFilesAddsPathsAndMargin(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	if err := os.WriteFile(a, []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte("xy"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := estimateFiles(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if got != 5+2+spaceMargin {
		t.Fatalf("estimate = %d", got)
	}
}
