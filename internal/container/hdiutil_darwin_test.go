//go:build darwin

package container

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestNewHdiutilCommandSkipsFailedConstructor(t *testing.T) {
	argv, consErr := HdiutilCreateArgv("", "zstd", "app", "t.dmg")
	if consErr == nil {
		t.Fatal("constructor succeeded")
	}
	cmd, err := NewHdiutilCommand(argv, consErr)
	if cmd != nil || err == nil {
		t.Fatalf("cmd=%v err=%v", cmd, err)
	}
}

func TestNewHdiutilCommandNamesSystemBinary(t *testing.T) {
	argv, err := HdiutilDetachArgv("/mnt")
	if err != nil {
		t.Fatal(err)
	}
	cmd, err := NewHdiutilCommand(argv, err)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(cmd.Path) != HdiutilPath || cmd.Args[0] != HdiutilPath {
		t.Fatalf("path = %q args = %q", cmd.Path, cmd.Args)
	}
	want := append([]string{HdiutilPath}, argv[1:]...)
	if !slices.Equal(cmd.Args, want) {
		t.Fatalf("args = %q", cmd.Args)
	}
	// Do not start cmd. Tests must not exec hdiutil.
}
