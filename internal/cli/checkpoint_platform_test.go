//go:build linux || darwin

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckpointCommandsRestoreDirectory(t *testing.T) {
	home, _ := withWorkspace(t)
	home, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, "agents")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(target, "skill")
	if err := os.WriteFile(file, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	out, stderr, _, err := runCmd(t, "checkpoint", "create", target)
	if err != nil || !strings.Contains(out, target+".checkpoint") {
		t.Fatalf("create: out=%q stderr=%q err=%v", out, stderr, err)
	}
	if err := os.WriteFile(file, []byte("after"), 0600); err != nil {
		t.Fatal(err)
	}
	out, stderr, _, err = runCmd(t, "checkpoint", "restore", "--rm", target)
	if err != nil || !strings.Contains(out, target) {
		t.Fatalf("restore: out=%q stderr=%q err=%v", out, stderr, err)
	}
	data, err := os.ReadFile(file)
	if err != nil || string(data) != "before" {
		t.Fatalf("restored data=%q err=%v", data, err)
	}
	if _, err := os.Lstat(target + ".checkpoint"); !os.IsNotExist(err) {
		t.Fatalf("checkpoint still exists: %v", err)
	}
}

func TestCheckpointMultipleArgumentsAndSuffix(t *testing.T) {
	home, _ := withWorkspace(t)
	home, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	one, two := filepath.Join(home, "one"), filepath.Join(home, "two.checkpoint")
	for _, dir := range []string{one, two} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "data"), []byte("original"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(home, "alias")
	if err := os.Symlink(one, link); err != nil {
		t.Fatal(err)
	}
	out, stderr, _, err := runCmd(t, "checkpoint", "create", link+".checkpoint", two)
	if err != nil || !strings.Contains(stderr, "warning:") || !strings.Contains(out, one+".checkpoint") || !strings.Contains(out, two+".checkpoint") {
		t.Fatalf("create: out=%q stderr=%q err=%v", out, stderr, err)
	}
	for _, dir := range []string{one, two} {
		if err := os.WriteFile(filepath.Join(dir, "data"), []byte("changed"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	out, stderr, _, err = runCmd(t, "checkpoint", "restore", "--rm", one+".checkpoint", two)
	if err != nil || !strings.Contains(stderr, "warning:") || !strings.Contains(out, one) || !strings.Contains(out, two) {
		t.Fatalf("restore: out=%q stderr=%q err=%v", out, stderr, err)
	}
	for _, dir := range []string{one, two} {
		data, err := os.ReadFile(filepath.Join(dir, "data"))
		if err != nil || string(data) != "original" {
			t.Fatalf("%s: %q %v", dir, data, err)
		}
	}
}

func TestCheckpointBatchRejectsOverlapBeforeChanges(t *testing.T) {
	home, _ := withWorkspace(t)
	one := filepath.Join(home, "one")
	if err := os.MkdirAll(filepath.Join(one, "child"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{one, filepath.Join(one, "child")}, {one, one + ".checkpoint"}} {
		_, _, _, err := runCmd(t, append([]string{"checkpoint", "create"}, args...)...)
		if err == nil || !strings.Contains(err.Error(), "overlapping") {
			t.Fatalf("%v: %v", args, err)
		}
		if _, err := os.Lstat(one + ".checkpoint"); !os.IsNotExist(err) {
			t.Fatalf("modified: %v", err)
		}
	}
}

func TestCheckpointBatchStopsAfterFailure(t *testing.T) {
	home, _ := withWorkspace(t)
	one, missing, last := filepath.Join(home, "one"), filepath.Join(home, "missing"), filepath.Join(home, "last")
	for _, dir := range []string{one, last} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	out, _, _, err := runCmd(t, "checkpoint", "create", one, missing, last)
	if err == nil || !strings.Contains(err.Error(), "not processed") || !strings.Contains(err.Error(), last) || !strings.Contains(out, one) {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if _, err := os.Stat(one + ".checkpoint"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(last + ".checkpoint"); !os.IsNotExist(err) {
		t.Fatalf("last processed: %v", err)
	}
}

func TestCheckpointSuffixInspectionErrorStopsBatch(t *testing.T) {
	home, _ := withWorkspace(t)
	first, loop := filepath.Join(home, "first"), filepath.Join(home, "loop")
	if err := os.Mkdir(first, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("loop", loop); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := runCmd(t, "checkpoint", "create", first, loop+".checkpoint")
	if err == nil || !strings.Contains(err.Error(), "inspect") {
		t.Fatalf("error = %v", err)
	}
	if _, err := os.Lstat(first + ".checkpoint"); !os.IsNotExist(err) {
		t.Fatalf("batch modified: %v", err)
	}
}

func TestCheckpointBatchIdempotentAndMissing(t *testing.T) {
	home, _ := withWorkspace(t)
	home, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	one, two, missing := filepath.Join(home, "one"), filepath.Join(home, "two"), filepath.Join(home, "missing")
	for _, dir := range []string{one, two, missing} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, _, err := runCmd(t, "checkpoint", "create", one); err != nil {
		t.Fatal(err)
	}
	out, _, _, err := runCmd(t, "checkpoint", "create", one, two)
	if err != nil || !strings.Contains(out, "checkpoint already exists "+one+".checkpoint") || !strings.Contains(out, "created checkpoint "+two+".checkpoint") {
		t.Fatalf("out=%q err=%v", out, err)
	}
	out, stderr, _, err := runCmd(t, "checkpoint", "restore", "--rm", missing, one, two)
	if err != nil || !strings.Contains(stderr, "warning: checkpoint missing for "+missing) || !strings.Contains(out, "restored "+one) || !strings.Contains(out, "restored "+two) {
		t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
	}
}

func TestCheckpointCreateRejectsUnmarkedExistingDirectory(t *testing.T) {
	home, _ := withWorkspace(t)
	target := filepath.Join(home, "target")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target+".checkpoint", 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(target+".checkpoint", "data")
	if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := runCmd(t, "checkpoint", "create", target); err == nil {
		t.Fatal("accepted unmarked directory")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "keep" {
		t.Fatalf("data=%q err=%v", got, err)
	}
}
