package cli

import "testing"

func TestRootCommandName(t *testing.T) {
	cmd := NewRootCmd()
	if cmd == nil {
		t.Fatal("root command is nil")
	}
	if cmd.Use != "tiler" {
		t.Fatalf("root command use = %q, want tiler", cmd.Use)
	}
}
