package checkpoint

import (
	"path/filepath"
	"testing"
	"time"
)

func TestArchiveNameUsesRestoreTime(t *testing.T) {
	target := filepath.Join(t.TempDir(), "active")
	marker, err := newTestMarker(target, time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	oldNow, oldLocal := archiveNow, time.Local
	t.Cleanup(func() { archiveNow, time.Local = oldNow, oldLocal })
	time.Local = time.FixedZone("test", 8*60*60)
	archiveNow = func() time.Time { return time.Date(2026, 6, 7, 8, 9, 10, 0, time.UTC) }
	got, err := archiveName(target, marker)
	if err != nil || got != target+"-20260607-160910" {
		t.Fatalf("archive = %q, error = %v", got, err)
	}
	if _, err := archiveName(target, Marker{}); err == nil {
		t.Fatal("accepted invalid marker")
	}
}
