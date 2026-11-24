//go:build linux

package checkpoint

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jiangxin/tiler/internal/config"
)

func restoreFixture(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	target := filepath.Join(home, "agents")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "file"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(target, home, config.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "file"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	return home, target
}
func TestRestoreRejectsReservedCheckpointSource(t *testing.T) {
	home, target := restoreFixture(t)
	_, err := Restore(target, home, config.Config{ImagesDir: target + ".checkpoint"}, false)
	if !errors.Is(err, config.ErrReservedPath) {
		t.Fatalf("expected reserved path error, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(target+".checkpoint", MarkerName)); err != nil {
		t.Fatalf("checkpoint marker changed: %v", err)
	}
}

func TestRestorePreservesOrRemovesArchive(t *testing.T) {
	for _, rm := range []bool{false, true} {
		t.Run(map[bool]string{false: "preserve", true: "remove"}[rm], func(t *testing.T) {
			home, target := restoreFixture(t)
			archive, err := Restore(target, home, config.Config{}, rm)
			if err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(filepath.Join(target, "file"))
			if err != nil || string(b) != "original" {
				t.Fatalf("restored %q %v", b, err)
			}
			if _, err = os.Lstat(target + ".checkpoint"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("checkpoint %v", err)
			}
			if _, err = os.Lstat(filepath.Join(target, MarkerName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("marker %v", err)
			}
			b, err = os.ReadFile(filepath.Join(archive, "file"))
			if rm {
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("archive %v", err)
				}
			} else if err != nil || string(b) != "changed" {
				t.Fatalf("archive %q %v", b, err)
			}
		})
	}
}
func TestRestoreMissingTargetAndInvalidMarker(t *testing.T) {
	home, target := restoreFixture(t)
	if err := os.RemoveAll(target); err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(target, home, config.Config{}, true); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(target, "file"))
	if err != nil || string(b) != "original" {
		t.Fatalf("restored %q %v", b, err)
	}
	home, target = restoreFixture(t)
	if err = os.WriteFile(filepath.Join(target+".checkpoint", MarkerName), []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Restore(target, home, config.Config{}, false); err == nil {
		t.Fatal("accepted bad marker")
	}
	b, err = os.ReadFile(filepath.Join(target, "file"))
	if err != nil || string(b) != "changed" {
		t.Fatalf("target changed %q %v", b, err)
	}
}
func TestRestoreRetriesPartialArchiveDeletion(t *testing.T) {
	home, target := restoreFixture(t)
	if err := os.WriteFile(filepath.Join(target, "another"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("partial deletion")
	restoreRemoveArchive = func(p *Parent, name string, want restoreIdentity) error {
		count := 0
		return removeArchiveWith(p, name, want, func(dir *os.File, entry string, flags uintptr) error {
			if count == 1 {
				return injected
			}
			count++
			return unlinkAt(dir, entry, flags)
		})
	}
	t.Cleanup(func() { restoreRemoveArchive = removeArchive })
	archive, err := Restore(target, home, config.Config{}, true)
	if !errors.Is(err, injected) {
		t.Fatalf("failure=%v", err)
	}
	_ = archive
	restoreRemoveArchive = removeArchive
	if _, err = Restore(target, home, config.Config{}, true); err != nil {
		t.Fatalf("retry: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(target, "file"))
	if err != nil || string(body) != "original" {
		t.Fatalf("restored=%q err=%v", body, err)
	}
}

func TestRestoreRejectsArchiveCollisionAndCorruptTransaction(t *testing.T) {
	t.Run("archive collision", func(t *testing.T) {
		home, target := restoreFixture(t)
		p, err := OpenParent(target)
		if err != nil {
			t.Fatal(err)
		}
		r, err := prepareExchangeRestore(p, target, false)
		_ = p.Close()
		if err != nil {
			t.Fatal(err)
		}
		if err = os.Mkdir(r.Archive, 0700); err != nil {
			t.Fatal(err)
		}
		if _, err = Restore(target, home, config.Config{}, false); !errors.Is(err, os.ErrExist) {
			t.Fatalf("error=%v", err)
		}
		body, err := os.ReadFile(filepath.Join(target, "file"))
		if err != nil || string(body) != "changed" {
			t.Fatalf("target=%q err=%v", body, err)
		}
		if _, err = os.Stat(target + ".checkpoint"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("corrupt transaction", func(t *testing.T) {
		home, target := restoreFixture(t)
		if err := os.WriteFile(filepath.Join(home, ".agents.tiler-checkpoint-restore.json"), []byte("broken"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Restore(target, home, config.Config{}, true); err == nil {
			t.Fatal("accepted corrupt transaction")
		}
		body, err := os.ReadFile(filepath.Join(target, "file"))
		if err != nil || string(body) != "changed" {
			t.Fatalf("target=%q err=%v", body, err)
		}
		if _, err = os.Stat(target + ".checkpoint"); err != nil {
			t.Fatal(err)
		}
	})
}
