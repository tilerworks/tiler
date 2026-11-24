//go:build linux

package checkpoint

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestExchangeStagePublishAndRetry(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "active")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	p, err := OpenParent(target)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	dir, err := openDirectory(target)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dir.Close() }()
	id, err := createExchangeStage(p, ".stage", []byte("marker"))
	if err != nil {
		t.Fatal(err)
	}
	if err = linkExchangeStage(p, ".stage", dir, ".marker", id); err != nil {
		t.Fatal(err)
	}
	// Simulate an interruption after publication and retry with the recorded id.
	if err = linkExchangeStage(p, ".stage", dir, ".marker", id); err != nil {
		t.Fatal(err)
	}
	if err = removeExchangeStage(p.File, ".stage", id); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(target, ".marker")); err != nil || string(data) != "marker" {
		t.Fatalf("marker = %q, %v", data, err)
	}
	if err = removeExchangeStage(dir, ".marker", id); err != nil {
		t.Fatal(err)
	}
	if err = removeExchangeStage(dir, ".marker", id); err != nil {
		t.Fatal(err)
	}
}

func TestExchangeStageRejectsUnknownAndReplacedFiles(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "active")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	p, err := OpenParent(target)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	dir, err := openDirectory(target)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dir.Close() }()
	id, err := createExchangeStage(p, ".stage", []byte("data"))
	if err != nil {
		t.Fatal(err)
	}
	if err = removeExchangeStage(p.File, ".stage", restoreIdentity{}); err == nil {
		t.Fatal("removed unrecorded stage")
	}
	if _, err = createExchangeStage(p, ".stage", nil); !errors.Is(err, syscall.EEXIST) {
		t.Fatalf("existing stage: %v", err)
	}
	if err = os.WriteFile(filepath.Join(target, ".marker"), []byte("other"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = linkExchangeStage(p, ".stage", dir, ".marker", id); err == nil {
		t.Fatal("accepted destination conflict")
	}
	if data, err := os.ReadFile(filepath.Join(target, ".marker")); err != nil || string(data) != "other" {
		t.Fatalf("conflict = %q, %v", data, err)
	}
	if err = os.Remove(filepath.Join(root, ".stage")); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(target, filepath.Join(root, ".stage")); err != nil {
		t.Fatal(err)
	}
	if err = removeExchangeStage(p.File, ".stage", id); err == nil {
		t.Fatal("removed replacement symlink")
	}
	if _, err = os.Lstat(filepath.Join(root, ".stage")); err != nil {
		t.Fatal(err)
	}
}

func TestExchangeStagePublishUsesVerifiedInode(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "active")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	p, err := OpenParent(target)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	dir, err := openDirectory(target)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dir.Close() }()
	id, err := createExchangeStage(p, ".stage", []byte("original"))
	if err != nil {
		t.Fatal(err)
	}
	originalLink := linkVerifiedExchangeStage
	linkVerifiedExchangeStage = func(source, dest *os.File, name string) error {
		if err := os.Rename(filepath.Join(root, ".stage"), filepath.Join(root, ".saved")); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(root, ".stage"), []byte("replacement"), 0600); err != nil {
			return err
		}
		return originalLink(source, dest, name)
	}
	defer func() { linkVerifiedExchangeStage = originalLink }()
	if err := linkExchangeStage(p, ".stage", dir, ".marker", id); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(target, ".marker"))
	if err != nil || string(data) != "original" {
		t.Fatalf("published %q, %v", data, err)
	}
}

func TestExchangeStagePublishFailsClosedWithoutDescriptorLink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "active")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	p, err := OpenParent(target)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	dir, err := openDirectory(target)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dir.Close() }()
	id, err := createExchangeStage(p, ".stage", []byte("original"))
	if err != nil {
		t.Fatal(err)
	}
	originalLink := linkVerifiedExchangeStage
	linkVerifiedExchangeStage = func(_, _ *os.File, _ string) error { return syscall.ENOENT }
	defer func() { linkVerifiedExchangeStage = originalLink }()
	if err := linkExchangeStage(p, ".stage", dir, ".marker", id); !errors.Is(err, syscall.ENOENT) {
		t.Fatalf("link error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(target, ".marker")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected destination: %v", err)
	}
}
