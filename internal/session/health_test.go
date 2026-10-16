package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHealthClassification(t *testing.T) {
	tests := []struct {
		name     string
		listed   bool
		live     bool
		readable bool
		want     bool
	}{
		{name: "listed entry and readable", listed: true, readable: true, want: true},
		{name: "stat live and readable", live: true, readable: true, want: true},
		{name: "listed but dead", listed: true, readable: false, want: false},
		{name: "live but dead connection", live: true, readable: false, want: false},
		{name: "vanished entry over a plain directory", readable: true, want: false},
		{name: "vanished", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classify(tc.listed, tc.live, tc.readable); got != tc.want {
				t.Fatalf("classify(%v, %v, %v) = %v, want %v",
					tc.listed, tc.live, tc.readable, got, tc.want)
			}
		})
	}
}

func TestCleanStaleToleratesPlainAndEmptyPoints(t *testing.T) {
	if err := CleanStale(""); err != nil {
		t.Fatalf("empty point: %v", err)
	}
	// A plain directory carries no mount: the unconditional unmount
	// attempts fail harmlessly and the point stays clean.
	plain := filepath.Join(t.TempDir(), "plain")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := CleanStale(plain); err != nil {
		t.Fatalf("plain point: %v", err)
	}
}
