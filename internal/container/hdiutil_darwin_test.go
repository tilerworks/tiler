//go:build darwin

package container

import (
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
	if cmd.Path != HdiutilPath {
		t.Fatalf("path = %q", cmd.Path)
	}
	want := append([]string{HdiutilPath}, argv[1:]...)
	if !slices.Equal(cmd.Args, want) {
		t.Fatalf("args = %q", cmd.Args)
	}
	// Do not start cmd. Tests must not exec /usr/bin/hdiutil.
}
