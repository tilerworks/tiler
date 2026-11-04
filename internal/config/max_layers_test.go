package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestMaxLayersDefaultsAndFileOverrides(t *testing.T) {
	home := t.TempDir()
	project := filepath.Join(home, "project")
	mustMkdir(t, project)
	load := func(want int) {
		t.Helper()
		cfg, err := Load(project, home, home, "", Patch{})
		if err != nil || cfg.MaxLayers != want || cfg.LayerLimit() != want {
			t.Fatalf("max layers = %d, want %d: %v", cfg.MaxLayers, want, err)
		}
	}
	load(32)
	writeConfig(t, home, "max_layers: 64\n")
	load(64)
	writeConfig(t, project, "max_layers: 12\n")
	load(12)
	writeConfig(t, project, "max_layers: 1\n")
	load(1)
	if got := (Config{}).LayerLimit(); got != 32 {
		t.Fatalf("zero-valued library config limit = %d", got)
	}
	writeConfig(t, project, "max_layers: 0\n")
	_, err := Load(project, home, home, "", Patch{})
	if err == nil || !strings.Contains(err.Error(), filepath.Join(project, fileName)) ||
		!strings.Contains(err.Error(), "max_layers: must be a positive integer") {
		t.Fatalf("invalid limit did not identify its file and field: %v", err)
	}
}

func TestMaxLayersRejectsInvalidValues(t *testing.T) {
	for _, value := range []string{"0", "-1", "1.5", "32.0", "many", "[32]", "{count: 32}"} {
		t.Run(value, func(t *testing.T) {
			_, err := applyDoc("max_layers: " + value + "\n")
			if err == nil || !strings.Contains(err.Error(), "max_layers") {
				t.Fatalf("invalid max_layers %q: %v", value, err)
			}
		})
	}
}
