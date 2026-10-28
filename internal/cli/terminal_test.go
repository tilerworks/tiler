package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestInputTerminalRejectsNonTerminalInput covers every input a pipeline or a
// script can hand to a command: a byte buffer, a pipe, a regular file, and the
// null device. None of them may wait for an answer.
func TestInputTerminalRejectsNonTerminalInput(t *testing.T) {
	if inputTerminal(strings.NewReader("yes\n")) {
		t.Fatal("a byte buffer reported an interactive terminal")
	}
	pipeIn, pipeOut, err := os.Pipe()
	if err != nil {
		t.Fatalf("create pipe: %v", err)
	}
	defer func() { _ = pipeIn.Close() }()
	defer func() { _ = pipeOut.Close() }()
	if inputTerminal(pipeIn) {
		t.Fatal("a pipe reported an interactive terminal")
	}
	path := filepath.Join(t.TempDir(), "answer")
	if err := os.WriteFile(path, []byte("yes\n"), 0o600); err != nil {
		t.Fatalf("write answer file: %v", err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open answer file: %v", err)
	}
	defer func() { _ = file.Close() }()
	if inputTerminal(file) {
		t.Fatal("a regular file reported an interactive terminal")
	}
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open null device: %v", err)
	}
	defer func() { _ = null.Close() }()
	if inputTerminal(null) {
		t.Fatal("the null device reported an interactive terminal")
	}
}

// TestInputTerminalFollowsTheCharacterDeviceMark keeps the accepting branch
// covered: any open character device other than the null device is the mark of
// an interactive stream on the supported hosts, so standard input from a
// terminal is not refused.
func TestInputTerminalFollowsTheCharacterDeviceMark(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the character device fixture is a Unix path")
	}
	zero, err := os.Open("/dev/zero")
	if err != nil {
		t.Skipf("no character device fixture: %v", err)
	}
	defer func() { _ = zero.Close() }()
	if !inputTerminal(zero) {
		t.Fatal("a character device that is not the null device was refused")
	}
}
