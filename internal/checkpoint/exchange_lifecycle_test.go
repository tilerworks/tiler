//go:build linux || darwin

package checkpoint

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/config"
	_ "github.com/jiangxin/tiler/internal/testutil"
	"golang.org/x/sys/unix"
)

func exchangeFixture(t *testing.T) (string, string) {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, "agents")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "file"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	return home, target
}

func exchangeRestoreFixture(t *testing.T) (string, string) {
	t.Helper()
	home, target := exchangeFixture(t)
	if _, err := Create(target, home, config.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "file"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	return home, target
}

func TestExchangeRestoreRetryRejectsNewArchiveBeforeSwap(t *testing.T) {
	home, target := exchangeRestoreFixture(t)
	p, err := OpenParent(target)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	injected := errors.New("interruption")
	exchangeCheckpoint = func(point string) error {
		if point == "before:exchange" {
			return injected
		}
		return nil
	}
	defer func() { exchangeCheckpoint = func(string) error { return nil } }()
	if _, err := Restore(target, home, config.Config{}, false); !errors.Is(err, injected) {
		t.Fatalf("interrupt restore: %v", err)
	}
	exchangeCheckpoint = func(string) error { return nil }
	record, _, err := readExchangeRecord(p, exchangeRecordName(target), target)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(record.Archive, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(target, home, config.Config{}, false); !errors.Is(err, os.ErrExist) {
		t.Fatalf("archive conflict: %v", err)
	}
	base := filepath.Base(target)
	if err := checkExchangePair(p, base, base+".checkpoint", record.Copy, record.Original); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(target+".checkpoint", MarkerName)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(record.Archive); err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(target, home, config.Config{}, false); err != nil {
		t.Fatal(err)
	}
}

func TestExchangeRestoreInstallsCheckpointWhenTargetIsMissing(t *testing.T) {
	home, target := exchangeRestoreFixture(t)
	if err := os.RemoveAll(target); err != nil {
		t.Fatal(err)
	}
	archive, err := Restore(target, home, config.Config{}, true)
	if err != nil || archive != "" {
		t.Fatalf("restore missing target returned archive %q: %v", archive, err)
	}
	data, err := os.ReadFile(filepath.Join(target, "file"))
	if err != nil || string(data) != "original" {
		t.Fatalf("restored file = %q: %v", data, err)
	}
	if _, err := os.Lstat(filepath.Join(target, MarkerName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("active marker remains: %v", err)
	}
}

func TestExchangeCreateRecoveryBoundaries(t *testing.T) {
	for _, point := range []string{
		"before:stage:create", "before:stage:link", "after:stage:link",
		"before:stage:remove-dest", "after:stage:remove-dest",
		"before:stage:remove-source", "after:stage:remove-source",
		"before:mkdir:temporary", "before:copy", "after:copy", "before:sync:copy", "after:sync:copy",
		"before:record:intent", "after:record:intent", "before:sync:parent", "after:sync:parent",
		"before:record:publish", "after:record:publish", "before:publish", "after:publish",
		"before:record:exchange", "after:record:exchange", "before:exchange", "after:exchange",
		"before:record:marker", "after:record:marker", "before:record:complete", "after:record:complete",
		"before:receipt:publish", "after:receipt:publish", "before:record:delete", "after:record:delete",
	} {
		t.Run(point, func(t *testing.T) {
			home, target := exchangeFixture(t)
			injected := errors.New("injected interruption")
			fired := false
			exchangeCheckpoint = func(got string) error {
				if got == point && !fired {
					fired = true
					return injected
				}
				return nil
			}
			defer func() { exchangeCheckpoint = func(string) error { return nil } }()
			_, err := Create(target, home, config.Config{})
			if !errors.Is(err, injected) {
				t.Fatalf("point %s: %v", point, err)
			}
			exchangeCheckpoint = func(string) error { return nil }
			if _, err = Create(target, home, config.Config{}); err != nil {
				t.Fatalf("retry %s: %v", point, err)
			}
			data, err := os.ReadFile(filepath.Join(target+".checkpoint", "file"))
			if err != nil || string(data) != "original" {
				t.Fatalf("checkpoint %q: %v", data, err)
			}
			if _, err = Restore(target, home, config.Config{}, true); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestExchangeCreatePreservesUnrecordedTemporary(t *testing.T) {
	for _, point := range []string{"after:stage:create", "after:mkdir:temporary"} {
		t.Run(point, func(t *testing.T) {
			home, target := exchangeFixture(t)
			injected := errors.New("interruption")
			exchangeCheckpoint = func(got string) error {
				if got == point {
					return injected
				}
				return nil
			}
			_, err := Create(target, home, config.Config{})
			exchangeCheckpoint = func(string) error { return nil }
			if !errors.Is(err, injected) {
				t.Fatal(err)
			}
			if _, err = Create(target, home, config.Config{}); err == nil {
				t.Fatal("accepted unrecorded object")
			}
			if _, err = os.Stat(filepath.Join(target, "file")); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestExchangeRestoreRecoveryBoundaries(t *testing.T) {
	for _, point := range []string{
		"before:record:prepared", "after:record:prepared", "before:sync:parent", "after:sync:parent",
		"before:record:reserve", "after:record:reserve", "before:record:install", "after:record:install", "before:exchange", "after:exchange",
		"before:restore:cleanup", "after:restore:cleanup", "before:record:cleanup", "after:record:cleanup",
		"before:record:marker", "after:record:marker", "before:restore:marker", "after:restore:marker",
		"before:record:archive", "after:record:archive", "before:restore:archive", "after:restore:archive",
		"before:record:remove", "after:record:remove", "before:restore:remove", "after:restore:remove",
		"before:record:complete", "after:record:complete", "before:receipt:publish", "after:receipt:publish", "before:record:delete", "after:record:delete",
	} {
		t.Run(point, func(t *testing.T) {
			home, target := exchangeRestoreFixture(t)
			injected := errors.New("interruption")
			fired := false
			exchangeCheckpoint = func(got string) error {
				if got == point && !fired {
					fired = true
					return injected
				}
				return nil
			}
			defer func() { exchangeCheckpoint = func(string) error { return nil } }()
			_, err := Restore(target, home, config.Config{}, true)
			if !errors.Is(err, injected) {
				t.Fatalf("point %s: %v", point, err)
			}
			exchangeCheckpoint = func(string) error { return nil }
			// The persisted --rm option wins even if the retry differs.
			archive, err := Restore(target, home, config.Config{}, point == "before:record:prepared")
			if err != nil {
				t.Fatalf("retry %s: %v", point, err)
			}
			data, err := os.ReadFile(filepath.Join(target, "file"))
			if err != nil || string(data) != "original" {
				t.Fatalf("restored %q: %v", data, err)
			}
			if _, err = os.Stat(archive); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("archive retained: %v", err)
			}
		})
	}
}

func TestExchangeKeepsOldOpenDirectoryHandle(t *testing.T) {
	home, target := exchangeFixture(t)
	dir, err := os.Open(target)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dir.Close() }()
	if _, err = Create(target, home, config.Config{}); err != nil {
		t.Fatal(err)
	}
	fd, err := unix.Openat(int(dir.Fd()), "held", unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC, 0600)
	if err != nil {
		t.Fatal(err)
	}
	held := os.NewFile(uintptr(fd), "held")
	_, writeErr := held.WriteString("via old handle")
	if err = errors.Join(writeErr, held.Close()); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(target+".checkpoint", "held")); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(target, "held")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("task copy changed: %v", err)
	}
}

func TestExchangeProcessCrashAndRetry(t *testing.T) {
	if target := os.Getenv("TILER_CHECKPOINT_CRASH_TARGET"); target != "" {
		point := os.Getenv("TILER_CHECKPOINT_CRASH_POINT")
		exchangeCheckpoint = func(got string) error {
			if got == point {
				os.Exit(79)
			}
			return nil
		}
		_, _ = Create(target, filepath.Dir(target), config.Config{})
		os.Exit(80)
	}
	for _, point := range []string{"after:exchange", "after:stage:create", "after:receipt:publish", "after:record:delete"} {
		t.Run(point, func(t *testing.T) {
			home, target := exchangeFixture(t)
			command := exec.Command(os.Args[0], "-test.run=^TestExchangeProcessCrashAndRetry$")
			command.Env = append(os.Environ(), "TILER_CHECKPOINT_CRASH_TARGET="+target, "TILER_CHECKPOINT_CRASH_POINT="+point)
			err := command.Run()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 79 {
				t.Fatalf("child: %v", err)
			}
			_, err = Create(target, home, config.Config{})
			if point == "after:stage:create" {
				if err == nil {
					t.Fatal("accepted unrecorded stage")
				}
				if _, err = os.Stat(filepath.Join(home, ".agents.tiler-checkpoint-stage-probe")); err != nil {
					t.Fatalf("unrecorded stage lost: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("retry: %v", err)
			}
			if _, err = Restore(target, home, config.Config{}, true); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestExchangeRejectsLegacyStateAndPreservesIt(t *testing.T) {
	t.Run("checkpoint", func(t *testing.T) {
		home, target := exchangeFixture(t)
		if err := os.Mkdir(target+".checkpoint", 0700); err != nil {
			t.Fatal(err)
		}
		markerPath := filepath.Join(target+".checkpoint", MarkerName)
		before, err := marshalTestMarker(Marker{Version: markerVersion, Target: target, CreatedAt: time.Now().UTC()})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(markerPath, before, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err = Restore(target, home, config.Config{}, false); err == nil {
			t.Fatal("accepted legacy checkpoint")
		}
		after, err := os.ReadFile(markerPath)
		if err != nil || string(after) != string(before) {
			t.Fatalf("legacy marker changed: %v", err)
		}
	})
	t.Run("transaction", func(t *testing.T) {
		home, target := exchangeFixture(t)
		record := filepath.Join(home, ".agents.tiler-checkpoint-create.json")
		if err := os.WriteFile(record, []byte("legacy"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Create(target, home, config.Config{}); err == nil {
			t.Fatal("accepted legacy transaction")
		}
		data, err := os.ReadFile(record)
		if err != nil || string(data) != "legacy" {
			t.Fatalf("legacy transaction changed: %v", err)
		}
	})
}

func TestExchangeCreateRetriesMarkerPublication(t *testing.T) {
	for _, point := range []string{"before:stage:link", "after:stage:link", "before:stage:remove-source", "after:stage:remove-source"} {
		t.Run(point, func(t *testing.T) {
			home, target := exchangeFixture(t)
			count := 0
			injected := errors.New("marker interruption")
			exchangeCheckpoint = func(got string) error {
				if got == point {
					count++
					if count == 2 {
						return injected
					}
				}
				return nil
			}
			defer func() { exchangeCheckpoint = func(string) error { return nil } }()
			if _, err := Create(target, home, config.Config{}); !errors.Is(err, injected) {
				t.Fatalf("interruption: %v", err)
			}
			exchangeCheckpoint = func(string) error { return nil }
			if _, err := Create(target, home, config.Config{}); err != nil {
				t.Fatalf("retry: %v", err)
			}
			if _, err := Restore(target, home, config.Config{}, true); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestExchangeCreateRejectsReadOnlyOriginalBeforeSwap(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory write permissions")
	}
	home, target := exchangeFixture(t)
	if err := os.Chmod(target, 0500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(target, 0700) }()
	before, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Create(target, home, config.Config{}); err == nil {
		t.Fatal("accepted read-only original")
	}
	after, err := os.Stat(target)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("original changed: %v", err)
	}
	if _, err = os.Lstat(target + ".checkpoint"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("checkpoint published: %v", err)
	}
}

func TestExchangeCreateRetriesRecordFailures(t *testing.T) {
	for _, point := range []string{"record:write", "record:sync-file", "record:publish", "record:replace", "record:sync-parent", "record:unlink"} {
		t.Run(point, func(t *testing.T) {
			home, target := exchangeFixture(t)
			injected := errors.New("record failure")
			fired := false
			exchangeRecordOperation = func(got string, operation func() error) error {
				if got == point && !fired {
					fired = true
					return injected
				}
				return operation()
			}
			defer func() { exchangeRecordOperation = func(_ string, operation func() error) error { return operation() } }()
			if _, err := Create(target, home, config.Config{}); !errors.Is(err, injected) {
				t.Fatalf("failure: %v", err)
			}
			exchangeRecordOperation = func(_ string, operation func() error) error { return operation() }
			if _, err := Create(target, home, config.Config{}); err != nil {
				t.Fatalf("retry: %v", err)
			}
			if _, err := Restore(target, home, config.Config{}, true); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestExchangeRejectsUnknownActiveMarkerBeforeRemoval(t *testing.T) {
	home, target := exchangeFixture(t)
	marker := filepath.Join(target, MarkerName)
	content := []byte(`{"version":99,"target":"/different"}`)
	if err := os.WriteFile(marker, content, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(target, home, config.Config{}); err == nil {
		t.Fatal("accepted unknown version")
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != string(content) {
		t.Fatalf("marker changed: %v", err)
	}
}

func TestExchangeMarkerFailureLeavesOriginalAndTaskForRetry(t *testing.T) {
	home, target := exchangeFixture(t)
	injected := errors.New("marker publication failed")
	count := 0
	exchangeCheckpoint = func(point string) error {
		if point == "before:stage:link" {
			count++
			if count == 2 {
				return injected
			}
		}
		return nil
	}
	defer func() { exchangeCheckpoint = func(string) error { return nil } }()
	if _, err := Create(target, home, config.Config{}); !errors.Is(err, injected) {
		t.Fatalf("failure: %v", err)
	}
	active, err := os.ReadFile(filepath.Join(target, "file"))
	if err != nil || string(active) != "original" {
		t.Fatalf("task: %q, %v", active, err)
	}
	original, err := os.ReadFile(filepath.Join(target+".checkpoint", "file"))
	if err != nil || string(original) != "original" {
		t.Fatalf("original: %q, %v", original, err)
	}
	exchangeCheckpoint = func(string) error { return nil }
	if _, err = Create(target, home, config.Config{}); err != nil {
		t.Fatal(err)
	}
}

func TestExchangeCreateRetryRemovesReadOnlyCopy(t *testing.T) {
	home, target := exchangeFixture(t)
	child := filepath.Join(target, "readonly")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child, "file"), []byte("content"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(child, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(child, 0700)
		_ = os.Chmod(filepath.Join(target+".checkpoint", "readonly"), 0700)
	})
	interrupted := errors.New("interrupted after copy")
	exchangeCheckpoint = func(point string) error {
		if point == "after:copy" {
			return interrupted
		}
		return nil
	}
	defer func() { exchangeCheckpoint = func(string) error { return nil } }()
	if _, err := Create(target, home, config.Config{}); !errors.Is(err, interrupted) {
		t.Fatalf("create: %v", err)
	}
	exchangeCheckpoint = func(string) error { return nil }
	if _, err := Create(target, home, config.Config{}); err != nil {
		t.Fatalf("retry: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(target+".checkpoint", "readonly", "file"))
	if err != nil || string(data) != "content" {
		t.Fatalf("original data %q: %v", data, err)
	}
}

func TestExchangeCreateReportsOldMarkerRemovalAfterInterruption(t *testing.T) {
	home, target := exchangeFixture(t)
	if err := os.WriteFile(filepath.Join(target, MarkerName), []byte("ordinary marker"), 0600); err != nil {
		t.Fatal(err)
	}
	interrupted := errors.New("interrupted after marker unlink")
	exchangeCheckpoint = func(point string) error {
		if point == "after:old-marker:unlink" {
			return interrupted
		}
		return nil
	}
	defer func() { exchangeCheckpoint = func(string) error { return nil } }()
	if _, err := Create(target, home, config.Config{}); !errors.Is(err, interrupted) || !strings.Contains(err.Error(), "existing "+MarkerName+" was removed") {
		t.Fatalf("create: %v", err)
	}
	exchangeCheckpoint = func(point string) error {
		if point == "before:copy" {
			return errors.New("copy failed")
		}
		return nil
	}
	_, err := Create(target, home, config.Config{})
	if err == nil || !strings.Contains(err.Error(), "existing "+MarkerName+" was removed") {
		t.Fatalf("missing removal report: %v", err)
	}
}

func TestExchangeCreateRetryRemovesInaccessibleCopy(t *testing.T) {
	home, target := exchangeFixture(t)
	child := filepath.Join(target, "nested")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child, "file"), []byte("content"), 0600); err != nil {
		t.Fatal(err)
	}
	interrupted := errors.New("interrupted after copy")
	copyChild := filepath.Join(home, ".agents.tiler-checkpoint-create-v2", "nested")
	defer func() {
		if err := os.Chmod(copyChild, 0700); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Error(err)
		}
	}()
	exchangeCheckpoint = func(point string) error {
		if point == "after:copy" {
			if err := os.Chmod(copyChild, 0000); err != nil {
				return err
			}
			return interrupted
		}
		return nil
	}
	defer func() { exchangeCheckpoint = func(string) error { return nil } }()
	if _, err := Create(target, home, config.Config{}); !errors.Is(err, interrupted) {
		t.Fatalf("create: %v", err)
	}
	exchangeCheckpoint = func(string) error { return nil }
	if runtime.GOOS == "darwin" && os.Geteuid() != 0 {
		if _, err := Create(target, home, config.Config{}); !errors.Is(err, os.ErrPermission) {
			t.Fatalf("inaccessible copy should be preserved: %v", err)
		}
		if err := os.Chmod(copyChild, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Create(target, home, config.Config{}); err != nil {
		t.Fatalf("retry: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(target+".checkpoint", "nested", "file"))
	if err != nil || string(data) != "content" {
		t.Fatalf("original data %q: %v", data, err)
	}
}

func TestExchangeOldMarkerProcessCrashReportsRemoval(t *testing.T) {
	if target := os.Getenv("TILER_MARKER_CRASH_TARGET"); target != "" {
		exchangeCheckpoint = func(point string) error {
			if point == "after:old-marker:unlink" {
				os.Exit(79)
			}
			return nil
		}
		_, _ = Create(target, filepath.Dir(target), config.Config{})
		os.Exit(80)
	}
	home, target := exchangeFixture(t)
	if err := os.WriteFile(filepath.Join(target, MarkerName), []byte("ordinary marker"), 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestExchangeOldMarkerProcessCrashReportsRemoval$")
	command.Env = append(os.Environ(), "TILER_MARKER_CRASH_TARGET="+target)
	var exit *exec.ExitError
	if err := command.Run(); !errors.As(err, &exit) || exit.ExitCode() != 79 {
		t.Fatalf("child: %v", err)
	}
	exchangeCheckpoint = func(point string) error {
		if point == "before:copy" {
			return errors.New("copy failed")
		}
		return nil
	}
	defer func() { exchangeCheckpoint = func(string) error { return nil } }()
	_, err := Create(target, home, config.Config{})
	if err == nil || !strings.Contains(err.Error(), "existing "+MarkerName+" was removed") {
		t.Fatalf("missing removal report: %v", err)
	}
}

func TestExchangeReceiptHandoffRetries(t *testing.T) {
	for _, point := range []string{"before:receipt:delete", "after:receipt:delete"} {
		t.Run(point, func(t *testing.T) {
			home, target := exchangeFixture(t)
			if _, err := Create(target, home, config.Config{}); err != nil {
				t.Fatal(err)
			}
			injected := errors.New("handoff interruption")
			exchangeCheckpoint = func(got string) error {
				if got == point {
					return injected
				}
				return nil
			}
			_, err := Restore(target, home, config.Config{}, true)
			exchangeCheckpoint = func(string) error { return nil }
			if !errors.Is(err, injected) {
				t.Fatalf("%s: %v", point, err)
			}
			if _, err = Restore(target, home, config.Config{}, true); err != nil {
				t.Fatal(err)
			}
			if _, err = Restore(target, home, config.Config{}, false); err != nil {
				t.Fatalf("restore replay: %v", err)
			}
			if _, err = Create(target, home, config.Config{}); err != nil {
				t.Fatalf("new create: %v", err)
			}
		})
	}
}

func TestExchangeReceiptRejectsChangedDirectory(t *testing.T) {
	home, target := exchangeFixture(t)
	if _, err := Create(target, home, config.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(target, target+".moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(target, home, config.Config{}); err == nil {
		t.Fatal("accepted replacement")
	}
	if _, err := Restore(target, home, config.Config{}, true); err == nil {
		t.Fatal("accepted replacement")
	}
	if _, err := os.Stat(target + ".checkpoint"); err != nil {
		t.Fatal(err)
	}
}

func TestExchangeRecordUnlinkSyncFailureReplays(t *testing.T) {
	for _, operation := range []string{"create", "restore"} {
		t.Run(operation, func(t *testing.T) {
			home, target := exchangeFixture(t)
			if operation == "restore" {
				if _, err := Create(target, home, config.Config{}); err != nil {
					t.Fatal(err)
				}
			}
			injected := errors.New("sync failure")
			removed := false
			armed := false
			exchangeCheckpoint = func(point string) error {
				if point == "before:record:delete" {
					armed = true
				}
				return nil
			}
			exchangeRecordOperation = func(point string, f func() error) error {
				if point == "record:unlink" && armed {
					if err := f(); err != nil {
						return err
					}
					removed = true
					return nil
				}
				if point == "record:sync-parent" && removed {
					return injected
				}
				return f()
			}
			var err error
			if operation == "create" {
				_, err = Create(target, home, config.Config{})
			} else {
				_, err = Restore(target, home, config.Config{}, true)
			}
			exchangeRecordOperation = func(_ string, f func() error) error { return f() }
			exchangeCheckpoint = func(string) error { return nil }
			if !errors.Is(err, injected) {
				t.Fatalf("sync failure: %v", err)
			}
			if operation == "create" {
				_, err = Create(target, home, config.Config{})
			} else {
				_, err = Restore(target, home, config.Config{}, true)
			}
			if err != nil {
				t.Fatalf("replay: %v", err)
			}
		})
	}
}

func TestExchangeRestoreProcessCrashAtReceipt(t *testing.T) {
	if target := os.Getenv("TILER_RECEIPT_CRASH_TARGET"); target != "" {
		point := os.Getenv("TILER_RECEIPT_CRASH_POINT")
		exchangeCheckpoint = func(got string) error {
			if got == point {
				os.Exit(79)
			}
			return nil
		}
		_, _ = Restore(target, filepath.Dir(target), config.Config{}, true)
		os.Exit(80)
	}
	for _, point := range []string{"after:receipt:delete", "after:receipt:publish", "after:record:delete"} {
		t.Run(point, func(t *testing.T) {
			home, target := exchangeFixture(t)
			if _, err := Create(target, home, config.Config{}); err != nil {
				t.Fatal(err)
			}
			command := exec.Command(os.Args[0], "-test.run=^TestExchangeRestoreProcessCrashAtReceipt$")
			command.Env = append(os.Environ(), "TILER_RECEIPT_CRASH_TARGET="+target, "TILER_RECEIPT_CRASH_POINT="+point)
			err := command.Run()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 79 {
				t.Fatalf("child: %v", err)
			}
			archive, err := Restore(target, home, config.Config{}, true)
			if err != nil {
				t.Fatalf("retry: %v", err)
			}
			if _, err = os.Stat(archive); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("archive retained: %v", err)
			}
			if _, err = Create(target, home, config.Config{}); err != nil {
				t.Fatalf("next create: %v", err)
			}
		})
	}
}

func TestExchangeReceiptHandoffAllowsActiveMarkerChanges(t *testing.T) {
	home, target := exchangeFixture(t)
	if _, err := Create(target, home, config.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, MarkerName), []byte("task marker"), 0600); err != nil {
		t.Fatal(err)
	}
	archive, err := Restore(target, home, config.Config{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(archive, MarkerName)); err != nil || string(data) != "task marker" {
		t.Fatalf("archived marker = %q, %v", data, err)
	}
	if err := os.WriteFile(filepath.Join(target, MarkerName), []byte("ordinary marker"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(target, home, config.Config{}); err != nil {
		t.Fatalf("create after marker change: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(target, MarkerName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ordinary marker should be removed: %v", err)
	}
}

func TestExchangeReceiptHandoffAllowsArchiveManagement(t *testing.T) {
	for _, action := range []string{"delete", "move", "reuse"} {
		t.Run(action, func(t *testing.T) {
			home, target := exchangeFixture(t)
			if _, err := Create(target, home, config.Config{}); err != nil {
				t.Fatal(err)
			}
			archive, err := Restore(target, home, config.Config{}, action == "reuse")
			if err != nil {
				t.Fatal(err)
			}
			switch action {
			case "delete":
				err = os.RemoveAll(archive)
			case "move":
				err = os.Rename(archive, archive+".moved")
			case "reuse":
				err = os.WriteFile(archive, []byte("unrelated"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Create(target, home, config.Config{}); err != nil {
				t.Fatalf("create after archive %s: %v", action, err)
			}
			if action == "reuse" {
				if data, err := os.ReadFile(archive); err != nil || string(data) != "unrelated" {
					t.Fatalf("reused archive = %q, %v", data, err)
				}
			}
		})
	}
}

func TestPlaceholderRestoreExchangeBoundaries(t *testing.T) {
	for _, nth := range []int{1, 2} {
		for _, before := range []bool{true, false} {
			t.Run(fmt.Sprintf("exchange-%d-before-%v", nth, before), func(t *testing.T) {
				home, target := exchangeRestoreFixture(t)
				count := 0
				injected := errors.New("interrupted exchange")
				exchangeCheckpoint = func(point string) error {
					if point == "before:exchange" {
						count++
					}
					if count == nth && ((before && point == "before:exchange") || (!before && point == "after:exchange")) {
						return injected
					}
					return nil
				}
				defer func() { exchangeCheckpoint = func(string) error { return nil } }()
				if _, err := Restore(target, home, config.Config{}, false); !errors.Is(err, injected) {
					t.Fatalf("interruption: %v", err)
				}
				exchangeCheckpoint = func(string) error { return nil }
				archive, err := Restore(target, home, config.Config{}, false)
				if err != nil {
					t.Fatal(err)
				}
				if data, err := os.ReadFile(filepath.Join(target, "file")); err != nil || string(data) != "original" {
					t.Fatalf("original %q: %v", data, err)
				}
				if _, err = os.Stat(archive); err != nil {
					t.Fatalf("archive: %v", err)
				}
				for _, path := range []string{target + ".checkpoint", target + ".checkpoint.lock", filepath.Join(target, MarkerName)} {
					if _, err = os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("unexpected %s: %v", path, err)
					}
				}
			})
		}
	}
}

func TestPlaceholderRestoreRejectsOccupiedPath(t *testing.T) {
	home, target := exchangeRestoreFixture(t)
	if err := os.Mkdir(target+".checkpoint.lock", 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(target, home, config.Config{}, false); err == nil {
		t.Fatal("accepted occupied placeholder")
	}
	if _, err := os.Stat(filepath.Join(target+".checkpoint", MarkerName)); err != nil {
		t.Fatal(err)
	}
}

func TestPlaceholderRestorePreservesUnrecordedDirectory(t *testing.T) {
	home, target := exchangeRestoreFixture(t)
	injected := errors.New("interruption after mkdir")
	exchangeCheckpoint = func(point string) error {
		if point == "after:restore:mkdir" {
			return injected
		}
		return nil
	}
	defer func() { exchangeCheckpoint = func(string) error { return nil } }()
	if _, err := Restore(target, home, config.Config{}, false); !errors.Is(err, injected) {
		t.Fatalf("interruption: %v", err)
	}
	exchangeCheckpoint = func(string) error { return nil }
	if _, err := Restore(target, home, config.Config{}, false); err == nil {
		t.Fatal("accepted unrecorded placeholder")
	}
	if _, err := os.Stat(target + ".checkpoint.lock"); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreRejectsLegacyExchangeRecord(t *testing.T) {
	home, target := exchangeRestoreFixture(t)
	_, parent, lock, err := exchangeContext(target, home, config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	record, err := prepareExchangeRestore(parent, target, false)
	if err != nil {
		t.Fatal(err)
	}
	record.Version, record.Protocol = 2, "exchange"
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(filepath.Dir(target), exchangeRecordName(target))
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	_ = lock.Close()
	_ = parent.Close()
	if _, err = Restore(target, home, config.Config{}, false); err == nil {
		t.Fatal("accepted old transaction")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(data) {
		t.Fatalf("old record changed: %v", err)
	}
	if _, err = os.Stat(target + ".checkpoint"); err != nil {
		t.Fatal(err)
	}
}

func TestPlaceholderRestoreRejectsChangedPlaceholder(t *testing.T) {
	for _, mutation := range []string{"replace", "populate"} {
		t.Run(mutation, func(t *testing.T) {
			home, target := exchangeRestoreFixture(t)
			injected := errors.New("stop before reserve")
			exchangeCheckpoint = func(point string) error {
				if point == "after:record:reserve" {
					return injected
				}
				return nil
			}
			defer func() { exchangeCheckpoint = func(string) error { return nil } }()
			if _, err := Restore(target, home, config.Config{}, false); !errors.Is(err, injected) {
				t.Fatalf("interruption: %v", err)
			}
			exchangeCheckpoint = func(string) error { return nil }
			path := target + ".checkpoint.lock"
			if mutation == "replace" {
				if err := os.Rename(path, path+".saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(filepath.Join(path, "foreign"), []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Restore(target, home, config.Config{}, false); err == nil {
				t.Fatal("accepted changed placeholder")
			}
			if _, err := os.Stat(filepath.Join(target+".checkpoint", MarkerName)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPlaceholderRestoreRefusesRemoveWhenOriginalMoved(t *testing.T) {
	home, target := exchangeRestoreFixture(t)
	injected := errors.New("stop before archive")
	exchangeCheckpoint = func(point string) error {
		if point == "after:record:archive" {
			return injected
		}
		return nil
	}
	defer func() { exchangeCheckpoint = func(string) error { return nil } }()
	if _, err := Restore(target, home, config.Config{}, true); !errors.Is(err, injected) {
		t.Fatalf("interruption: %v", err)
	}
	exchangeCheckpoint = func(string) error { return nil }
	if err := os.Rename(target, target+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(target+".checkpoint", target); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(target+".saved", target+".checkpoint"); err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(target, home, config.Config{}, true); err == nil {
		t.Fatal("accepted misplaced original")
	}
	if _, err := os.Stat(filepath.Join(target+".checkpoint.lock", "file")); err != nil {
		t.Fatalf("task directory lost: %v", err)
	}
}

func TestPlaceholderRestorePreservesMarkerOnSiblingConflict(t *testing.T) {
	for _, suffix := range []string{".checkpoint", ".checkpoint.lock"} {
		t.Run(suffix, func(t *testing.T) {
			home, target := exchangeRestoreFixture(t)
			injected := errors.New("stop before marker")
			exchangeCheckpoint = func(point string) error {
				if point == "after:record:marker" {
					return injected
				}
				return nil
			}
			defer func() { exchangeCheckpoint = func(string) error { return nil } }()
			if _, err := Restore(target, home, config.Config{}, false); !errors.Is(err, injected) {
				t.Fatalf("interruption: %v", err)
			}
			exchangeCheckpoint = func(string) error { return nil }
			if err := os.Mkdir(target+suffix, 0700); err != nil {
				t.Fatal(err)
			}
			if _, err := Restore(target, home, config.Config{}, false); err == nil {
				t.Fatal("accepted occupied sibling")
			}
			if _, err := os.Stat(filepath.Join(target, MarkerName)); err != nil {
				t.Fatalf("marker lost: %v", err)
			}
		})
	}
}

// TestCheckpointProcessInterruption is a test-only driver used by shell tests.
func TestCheckpointProcessInterruption(t *testing.T) {
	if os.Getenv("TILER_CHECKPOINT_TEST_DRIVER") != "1" {
		t.Skip("shell interruption driver")
	}
	point := os.Getenv("TILER_CHECKPOINT_TEST_POINT")
	count := 0
	exchangeCheckpoint = func(got string) error {
		if got == point {
			count++
			if count == 1 {
				os.Exit(77)
			}
		}
		return nil
	}
	target := os.Getenv("TILER_CHECKPOINT_TEST_TARGET")
	home := os.Getenv("HOME")
	var err error
	switch os.Getenv("TILER_CHECKPOINT_TEST_OPERATION") {
	case "create":
		_, err = Create(target, home, config.Config{})
	case "restore":
		_, err = Restore(target, home, config.Config{}, false)
	default:
		t.Fatal("unknown test operation")
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Fatal("interruption point was not reached")
}

func TestRestoreKeepsArchiveTimeAfterInterruption(t *testing.T) {
	home, target := exchangeRestoreFixture(t)
	oldNow := archiveNow
	t.Cleanup(func() { archiveNow = oldNow; exchangeCheckpoint = func(string) error { return nil } })
	first := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	archiveNow = func() time.Time { return first }
	injected := errors.New("interrupted")
	exchangeCheckpoint = func(point string) error {
		if point == "after:record:archive" {
			return injected
		}
		return nil
	}
	if _, err := Restore(target, home, config.Config{}, false); !errors.Is(err, injected) {
		t.Fatalf("interruption: %v", err)
	}
	exchangeCheckpoint = func(string) error { return nil }
	archiveNow = func() time.Time { return first.Add(24 * time.Hour) }
	archive, err := Restore(target, home, config.Config{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if want := target + "-" + first.Local().Format("20060102-150405"); archive != want {
		t.Fatalf("archive=%q want=%q", archive, want)
	}
}

func TestLegacyTransactionsArePreservedByBothCommands(t *testing.T) {
	for _, suffix := range []string{".tiler-checkpoint-create", ".tiler-checkpoint-create.json", ".tiler-checkpoint-create.json.new", ".tiler-checkpoint-restore.json"} {
		for _, operation := range []string{"create", "restore"} {
			t.Run(suffix+"/"+operation, func(t *testing.T) {
				home, target := exchangeFixture(t)
				path := filepath.Join(home, "."+filepath.Base(target)+suffix)
				if err := os.WriteFile(path, []byte("legacy transaction"), 0600); err != nil {
					t.Fatal(err)
				}
				var err error
				if operation == "create" {
					_, err = Create(target, home, config.Config{})
				} else {
					_, err = Restore(target, home, config.Config{}, false)
				}
				if err == nil {
					t.Fatal("accepted legacy transaction")
				}
				data, err := os.ReadFile(path)
				if err != nil || string(data) != "legacy transaction" {
					t.Fatalf("record=%q err=%v", data, err)
				}
				data, err = os.ReadFile(filepath.Join(target, "file"))
				if err != nil || string(data) != "original" {
					t.Fatalf("original=%q err=%v", data, err)
				}
				if _, err := os.Lstat(target + ".checkpoint"); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("checkpoint changed: %v", err)
				}
			})
		}
	}
}
