//go:build darwin

package workspace

import (
	"path/filepath"
	"testing"

	"github.com/jiangxin/tiler/internal/container"
)

func TestDarwinMountIsAvailable(t *testing.T) {
	if err := available(); err != nil {
		t.Fatal(err)
	}
}

func TestDarwinWorkspaceUsesSystemHdiutil(t *testing.T) {
	argv, err := container.HdiutilDetachArgv("/mnt")
	if err != nil {
		t.Fatal(err)
	}
	cmd, err := newHdiutil(argv, nil)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(cmd.Path) != container.HdiutilPath || cmd.Args[0] != container.HdiutilPath {
		t.Fatalf("path = %q args = %q", cmd.Path, cmd.Args)
	}
	if cmd.Process != nil {
		t.Fatal("command was started")
	}
}
