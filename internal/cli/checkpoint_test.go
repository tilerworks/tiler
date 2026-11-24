package cli

import (
	"strings"
	"testing"
)

func TestCheckpointCommandUsage(t *testing.T) {
	withWorkspace(t)
	for _, args := range [][]string{{"checkpoint", "create"}, {"checkpoint", "restore"}} {
		_, _, _, err := runCmd(t, args...)
		if ExitCode(err) != 2 {
			t.Errorf("%v: error = %v, want usage error", args, err)
		}
	}
	out, _, _, err := runCmd(t, "checkpoint", "restore", "--help")
	if err != nil || !strings.Contains(out, "--rm") {
		t.Fatalf("help = %q, error = %v", out, err)
	}
}
