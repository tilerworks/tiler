//go:build linux || darwin

package checkpoint

import (
	"errors"
	"os"
	"syscall"
	"testing"
)

func TestClonePolicyCachesOnlyConfirmedUnsupported(t *testing.T) {
	for _, tt := range []struct {
		name         string
		err          error
		confirmed    bool
		wantFallback bool
		wantCache    bool
	}{
		{"system unavailable", syscall.ENOSYS, false, true, true},
		{"filesystem confirmed", syscall.EOPNOTSUPP, true, true, true},
		{"file unsupported only", syscall.EOPNOTSUPP, false, true, false},
		{"invalid file", syscall.EINVAL, true, false, false},
		{"different device", syscall.EXDEV, true, false, false},
		{"permission error", syscall.EACCES, true, false, false},
		{"io error", syscall.EIO, true, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var policy clonePolicy
			fallback, err := policy.fallback(tt.err, func() (bool, error) { return tt.confirmed, nil })
			if err != nil || fallback != tt.wantFallback || policy.unavailable != tt.wantCache {
				t.Fatalf("fallback=%v cache=%v err=%v", fallback, policy.unavailable, err)
			}
			var nextCopy clonePolicy
			if nextCopy.unavailable {
				t.Fatal("capability leaked to another tree")
			}
		})
	}
}

func TestClonePolicyPreservesProbeCleanupFailure(t *testing.T) {
	var policy clonePolicy
	failure := errors.New("cleanup failed")
	_, err := policy.fallback(syscall.EOPNOTSUPP, func() (bool, error) { return false, failure })
	if !errors.Is(err, failure) || policy.unavailable {
		t.Fatalf("err=%v cache=%v", err, policy.unavailable)
	}
}

func TestCloneProbeCleansTemporaryFiles(t *testing.T) {
	dir, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dir.Close() }()
	unsupported, err := probeClone(dir, dir, func(_, _ *os.File, _ string) error { return syscall.EOPNOTSUPP })
	if err != nil || !unsupported {
		t.Fatalf("unsupported=%v err=%v", unsupported, err)
	}
	entries, err := dir.Readdirnames(-1)
	if err != nil || len(entries) != 0 {
		t.Fatalf("entries=%v err=%v", entries, err)
	}
}
