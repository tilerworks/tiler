//go:build linux

package mountinfo

import "testing"

func TestLoadParsesHostTable(t *testing.T) {
	mounts, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(mounts) == 0 {
		t.Fatal("host mount table is empty")
	}
	found := false
	for _, mount := range mounts {
		if mount.Point == "/" {
			found = true
		}
	}
	if !found {
		t.Error("root mount point missing from the parsed table")
	}
}
