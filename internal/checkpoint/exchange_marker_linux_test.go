//go:build linux

package checkpoint

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// requireAnonymousMarker checks both anonymous creation and publication on the
// filesystem used for test fixtures. Other failures remain test failures.
func requireAnonymousMarker(t *testing.T) {
	t.Helper()
	path := t.TempDir()
	dir, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dir.Close() }()
	marker, err := newExchangeMarker(filepath.Join(path, "target"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err = writeExchangeMarker(dir, marker); errors.Is(err, errAnonymousMarkerUnsupported) {
		t.Skipf("anonymous marker publication unsupported: %v", err)
	} else if err != nil {
		t.Fatalf("check anonymous marker support: %v", err)
	}
}

func TestExchangeMarkerAnonymousUnsupported(t *testing.T) {
	path := t.TempDir()
	dir, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dir.Close() }()
	marker, err := newExchangeMarker(filepath.Join(path, "target"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	original := exchangeMarkerOperation
	defer func() { exchangeMarkerOperation = original }()
	exchangeMarkerOperation = func(name string, operation func() error) error {
		if name == "probe:create" || name == "marker:create" {
			return errAnonymousMarkerUnsupported
		}
		return operation()
	}
	if err = probeExchangeMarkerWrite(dir); !errors.Is(err, errAnonymousMarkerUnsupported) {
		t.Fatalf("probe error = %v", err)
	}
	if err = writeExchangeMarker(dir, marker); !errors.Is(err, errAnonymousMarkerUnsupported) {
		t.Fatalf("write error = %v", err)
	}
}

func TestExchangeMarkerOperations(t *testing.T) {
	requireAnonymousMarker(t)
	path := t.TempDir()
	dir, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dir.Close() }()
	marker, err := newExchangeMarker(filepath.Join(path, "target"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err = probeExchangeMarkerWrite(dir); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(path)
	if err != nil || len(entries) != 0 {
		t.Fatalf("probe left entries %v: %v", entries, err)
	}
	if err = writeExchangeMarker(dir, marker); err != nil {
		t.Fatal(err)
	}
	got, err := readExchangeMarker(dir, marker.Target)
	if err != nil || !got.CreatedAt.Equal(marker.CreatedAt) {
		t.Fatalf("read marker: %+v, %v", got, err)
	}
	if err = writeExchangeMarker(dir, marker); err != nil {
		t.Fatalf("retry: %v", err)
	}
	other := marker
	other.CreatedAt = other.CreatedAt.Add(time.Second)
	if err = writeExchangeMarker(dir, other); err == nil {
		t.Fatal("accepted different marker")
	}
	if err = removeExchangeMarker(dir, other); err == nil {
		t.Fatal("removed different marker")
	}
	if err = removeExchangeMarker(dir, marker); err != nil {
		t.Fatal(err)
	}
	if err = removeExchangeMarker(dir, marker); err != nil {
		t.Fatalf("removal retry: %v", err)
	}
}

func TestExchangeMarkerRejectsUntrustedObjects(t *testing.T) {
	requireAnonymousMarker(t)
	for _, kind := range []string{"symlink", "directory", "invalid", "v1"} {
		t.Run(kind, func(t *testing.T) {
			path := t.TempDir()
			dir, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = dir.Close() }()
			name := filepath.Join(path, MarkerName)
			switch kind {
			case "symlink":
				err = os.Symlink("missing", name)
			case "directory":
				err = os.Mkdir(name, 0700)
			case "invalid":
				err = os.WriteFile(name, []byte("{"), 0600)
			case "v1":
				err = os.WriteFile(name, []byte(`{"version":1}`), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			marker, err := newExchangeMarker(filepath.Join(path, "target"), time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if err = writeExchangeMarker(dir, marker); err == nil {
				t.Fatal("accepted marker conflict")
			}
			if err = removeExchangeMarker(dir, marker); err == nil {
				t.Fatal("removed untrusted marker")
			}
			if _, err = os.Lstat(name); err != nil {
				t.Fatalf("marker not preserved: %v", err)
			}
		})
	}
}

func TestExchangeMarkerDurabilityFailures(t *testing.T) {
	requireAnonymousMarker(t)
	for _, stage := range []string{"marker:create", "marker:created", "marker:write", "marker:sync-file", "marker:publish", "marker:sync-dir"} {
		t.Run(stage, func(t *testing.T) {
			path := t.TempDir()
			dir, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = dir.Close() }()
			marker, err := newExchangeMarker(filepath.Join(path, "target"), time.Now())
			if err != nil {
				t.Fatal(err)
			}
			injected := errors.New("injected")
			original := exchangeMarkerOperation
			exchangeMarkerOperation = func(name string, operation func() error) error {
				if name == stage {
					return injected
				}
				return operation()
			}
			defer func() { exchangeMarkerOperation = original }()
			if err = writeExchangeMarker(dir, marker); !errors.Is(err, injected) {
				t.Fatalf("got %v", err)
			}
			exchangeMarkerOperation = original
			if err = writeExchangeMarker(dir, marker); err != nil {
				t.Fatalf("retry: %v", err)
			}
		})
	}
}

func TestExchangeMarkerPartialWriteCanRetry(t *testing.T) {
	requireAnonymousMarker(t)
	path := t.TempDir()
	dir, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dir.Close() }()
	marker, err := newExchangeMarker(filepath.Join(path, "target"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("partial write")
	original := exchangeMarkerOperation
	defer func() { exchangeMarkerOperation = original }()
	exchangeMarkerOperation = func(name string, operation func() error) error {
		if name != "marker:write" {
			return operation()
		}
		// The anonymous inode is never reachable through a directory entry.
		entries, readErr := os.ReadDir(path)
		if readErr != nil || len(entries) != 0 {
			t.Fatalf("temporary entry: %v, %v", entries, readErr)
		}
		return injected
	}
	if err = writeExchangeMarker(dir, marker); !errors.Is(err, injected) {
		t.Fatalf("got %v", err)
	}
	if _, err = os.Lstat(filepath.Join(path, MarkerName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial marker was published: %v", err)
	}
	exchangeMarkerOperation = original
	if err = writeExchangeMarker(dir, marker); err != nil {
		t.Fatalf("retry: %v", err)
	}
}

func TestExchangeMarkerProbeErrors(t *testing.T) {
	requireAnonymousMarker(t)
	path := t.TempDir()
	dir, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dir.Close() }()
	injected := errors.New("sync failed")
	original := exchangeMarkerOperation
	exchangeMarkerOperation = func(name string, operation func() error) error {
		if name == "probe:sync" {
			return injected
		}
		return operation()
	}
	defer func() { exchangeMarkerOperation = original }()
	if err = probeExchangeMarkerWrite(dir); !errors.Is(err, injected) {
		t.Fatalf("got %v", err)
	}
	entries, err := os.ReadDir(path)
	if err != nil || len(entries) != 0 {
		t.Fatalf("probe left entries %v: %v", entries, err)
	}
	if err = dir.Close(); err != nil {
		t.Fatal(err)
	}
	if err = probeExchangeMarkerWrite(dir); err == nil {
		t.Fatal("probe accepted closed directory")
	}
}

func TestExchangeMarkerRemovalInterrupted(t *testing.T) {
	requireAnonymousMarker(t)
	for _, stage := range []string{"marker:unlink", "marker:sync-dir"} {
		t.Run(stage, func(t *testing.T) {
			path := t.TempDir()
			dir, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = dir.Close() }()
			marker, err := newExchangeMarker(filepath.Join(path, "target"), time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if err = writeExchangeMarker(dir, marker); err != nil {
				t.Fatal(err)
			}
			injected := errors.New("injected")
			original := exchangeMarkerOperation
			exchangeMarkerOperation = func(name string, operation func() error) error {
				if name == stage {
					return injected
				}
				return operation()
			}
			defer func() { exchangeMarkerOperation = original }()
			if err = removeExchangeMarker(dir, marker); !errors.Is(err, injected) {
				t.Fatalf("got %v", err)
			}
			exchangeMarkerOperation = original
			if err = removeExchangeMarker(dir, marker); err != nil {
				t.Fatalf("retry: %v", err)
			}
		})
	}
}

func TestExchangeMarkerProbeReadOnly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write to a read-only directory")
	}
	path := t.TempDir()
	dir, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dir.Close() }()
	if err = os.Chmod(path, 0500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(path, 0700) }()
	if err = probeExchangeMarkerWrite(dir); !errors.Is(err, syscall.EACCES) {
		t.Fatalf("got %v", err)
	}
}

func TestExchangeMarkerCrashLeavesNoTemporaryEntry(t *testing.T) {
	if stage := os.Getenv("TILER_MARKER_CRASH_STAGE"); stage != "" {
		path := os.Getenv("TILER_MARKER_CRASH_DIR")
		dir, err := os.Open(path)
		if err != nil {
			os.Exit(2)
		}
		exchangeMarkerOperation = func(name string, op func() error) error {
			if name == stage {
				os.Exit(73)
			}
			return op()
		}
		if stage == "probe:created" {
			_ = probeExchangeMarkerWrite(dir)
		} else {
			marker, _ := newExchangeMarker(filepath.Join(path, "target"), time.Unix(1700000000, 0))
			_ = writeExchangeMarker(dir, marker)
		}
		os.Exit(3)
	}
	requireAnonymousMarker(t)
	for _, stage := range []string{"probe:created", "marker:created", "marker:sync-file", "marker:publish"} {
		t.Run(stage, func(t *testing.T) {
			path := t.TempDir()
			cmd := exec.Command(os.Args[0], "-test.run=^TestExchangeMarkerCrashLeavesNoTemporaryEntry$")
			cmd.Env = append(os.Environ(), "TILER_MARKER_CRASH_STAGE="+stage, "TILER_MARKER_CRASH_DIR="+path)
			err := cmd.Run()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 73 {
				t.Fatalf("child exit: %v", err)
			}
			entries, err := os.ReadDir(path)
			if err != nil || len(entries) != 0 {
				t.Fatalf("crash left entries: %v, %v", entries, err)
			}
			dir, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = dir.Close() }()
			marker, err := newExchangeMarker(filepath.Join(path, "target"), time.Unix(1700000000, 0))
			if err != nil {
				t.Fatal(err)
			}
			if err = writeExchangeMarker(dir, marker); err != nil {
				t.Fatalf("retry: %v", err)
			}
		})
	}
}

func TestExchangeMarkerRemovalRejectsReplacement(t *testing.T) {
	requireAnonymousMarker(t)
	path := t.TempDir()
	dir, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dir.Close() }()
	marker, err := newExchangeMarker(filepath.Join(path, "target"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err = writeExchangeMarker(dir, marker); err != nil {
		t.Fatal(err)
	}
	original := exchangeMarkerOperation
	defer func() { exchangeMarkerOperation = original }()
	exchangeMarkerOperation = func(name string, op func() error) error {
		if name == "marker:unlink" {
			if e := os.Rename(filepath.Join(path, MarkerName), filepath.Join(path, "saved")); e != nil {
				t.Fatal(e)
			}
			if e := os.WriteFile(filepath.Join(path, MarkerName), []byte("replacement"), 0600); e != nil {
				t.Fatal(e)
			}
		}
		return op()
	}
	if err = removeExchangeMarker(dir, marker); err == nil {
		t.Fatal("removed replacement")
	}
	data, err := os.ReadFile(filepath.Join(path, MarkerName))
	if err != nil || string(data) != "replacement" {
		t.Fatalf("replacement lost: %q %v", data, err)
	}
}
