//go:build linux

package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSessionForRuntimePathAfterEntryVanished(t *testing.T) {
	dir := t.TempDir()
	runtime := filepath.Join(dir, "runtime")
	view := filepath.Join(dir, "view")
	if err := os.MkdirAll(view, 0o755); err != nil {
		t.Fatal(err)
	}
	// The merged point's device and inode name its runtime
	// directory, the reverse link the stale fallback walks when the
	// table entry is gone.
	runtimeName, ok := runtimeNameOf(view)
	if !ok {
		t.Fatal("the fixture view has no runtime name")
	}
	runtimeDir := filepath.Join(runtime, runtimeName)
	if err := os.MkdirAll(filepath.Join(runtimeDir, "upper"), 0o755); err != nil {
		t.Fatal(err)
	}

	e := newEngine(filepath.Join(dir, "sessions"))
	e.RuntimeDir = runtime
	e.LoadTable = nil
	seedMounted(t, e, "dev", view)

	// The real probes classify the plain directory as dead, so the
	// hit reports the stale shape.
	hit, err := e.SessionForPath(filepath.Join(runtimeDir, "upper", "new.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if hit.Record.Name != "dev" || hit.Mountpoint != view || !hit.Stale {
		t.Fatalf("hit = %+v", hit)
	}
	// Another runtime directory links to no session.
	if _, err := e.SessionForPath(filepath.Join(runtime, "up-999-999", "upper")); err == nil {
		t.Fatal("a foreign runtime directory resolved")
	}
}
