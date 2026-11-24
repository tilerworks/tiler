//go:build linux

package checkpoint

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/jiangxin/tiler/internal/config"
)

func TestCreatePublishesIndependentCheckpoint(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, "agents")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "file"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, MarkerName), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := Create(target, home, config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if got != target+".checkpoint" {
		t.Fatal(got)
	}
	if _, err = os.Stat(filepath.Join(target, MarkerName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old marker: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(got, MarkerName))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = parseExchangeMarker(data, target); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(target, "file"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(filepath.Join(got, "file"))
	if err != nil || string(data) != "original" {
		t.Fatalf("checkpoint data %q: %v", data, err)
	}
	if again, err := Create(target, home, config.Config{}); err != nil || again != got {
		t.Fatalf("completed create replay: %q, %v", again, err)
	}
}

func TestCreateFailureCleansTemporaryAndReportsMarkerRemoval(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, "agents")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, MarkerName), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(target, "fifo"), 0700); err != nil {
		t.Fatal(err)
	}
	// A socket is unsupported and forces a failure after marker removal.
	if err := os.Remove(filepath.Join(target, "fifo")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(target, "fifo"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := Create(target, home, config.Config{})
	if err == nil || !strings.Contains(err.Error(), "was removed") {
		t.Fatalf("failure: %v", err)
	}
	for _, p := range []string{target + ".checkpoint", filepath.Join(home, ".agents.tiler-checkpoint-create"), filepath.Join(home, ".agents.tiler-checkpoint-create.json")} {
		if _, e := os.Lstat(p); !errors.Is(e, os.ErrNotExist) {
			t.Errorf("left %s: %v", p, e)
		}
	}
}

func TestCreateRejectsExistingCheckpointAsSource(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, "agents")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "data"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(target, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(target, home, config.Config{}); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(target+".checkpoint", home, config.Config{}); err == nil {
		t.Fatal("accepted an existing checkpoint as source")
	}
	if _, err := Create(filepath.Join(target+".checkpoint", "nested"), home, config.Config{}); err == nil {
		t.Fatal("accepted a directory inside an existing checkpoint")
	}
	if _, err := Restore(target, home, config.Config{}, false); err != nil {
		t.Fatalf("restore after rejected create: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(target, "data"))
	if err != nil || string(data) != "original" {
		t.Fatalf("restored data = %q, err = %v", data, err)
	}
}

func TestCreateRejectsReservedCheckpointDestination(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, "agents")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	_, err := Create(target, home, config.Config{ImagesDir: target + ".checkpoint"})
	if !errors.Is(err, config.ErrReservedPath) {
		t.Fatalf("expected reserved path error, got %v", err)
	}
	if _, err := os.Lstat(target + ".checkpoint"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reserved destination created: %v", err)
	}
}
