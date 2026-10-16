//go:build linux

package session

import (
	"path/filepath"
	"testing"

	"github.com/jiangxin/tiler/internal/mountinfo"
)

func TestHealthyRejectsPlainAndMissingPoints(t *testing.T) {
	plain := t.TempDir()
	missing := filepath.Join(plain, "absent")
	if ok, err := Healthy(nil, plain); ok || err != nil {
		t.Fatalf("plain directory: healthy = %v, err = %v", ok, err)
	}
	if ok, err := Healthy(nil, missing); ok || err != nil {
		t.Fatalf("missing point: healthy = %v, err = %v", ok, err)
	}
}

func TestHealthyClassifiesTableShapes(t *testing.T) {
	dir := t.TempDir()
	// Listed in the table and readable: the two-condition rule says
	// healthy, the same verdict a live view gets.
	listed := []mountinfo.Mount{{Point: dir, FSType: "fuse.fuse-overlayfs"}}
	if ok, err := Healthy(listed, dir); !ok || err != nil {
		t.Fatalf("listed readable point: healthy = %v, err = %v", ok, err)
	}
	// Listed but the directory is gone: the read probe refuses, so
	// the point is stale, not healthy.
	gone := filepath.Join(t.TempDir(), "absent")
	vanished := []mountinfo.Mount{{Point: gone, FSType: "fuse.fuse-overlayfs"}}
	if ok, err := Healthy(vanished, gone); ok || err != nil {
		t.Fatalf("listed dead point: healthy = %v, err = %v", ok, err)
	}
	// A plain directory the table does not list is not a mount.
	if ok, err := Healthy(vanished, dir); ok || err != nil {
		t.Fatalf("unlisted plain point: healthy = %v, err = %v", ok, err)
	}
}

func TestStillAttachedRejectsPlainPoints(t *testing.T) {
	if stillAttached(filepath.Join(t.TempDir(), "absent")) {
		t.Fatal("a missing point counts as attached")
	}
	if stillAttached(t.TempDir()) {
		t.Fatal("a plain directory counts as attached")
	}
}
