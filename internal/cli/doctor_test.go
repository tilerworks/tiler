package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/doctor"
)

func TestDoctorPrintsEveryProbeName(t *testing.T) {
	if _, err := doctor.DefaultSeams(); err != nil {
		t.Skip(err.Error())
	}
	withWorkspace(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stdout, stderr, err := runDoctorCmd(ctx, t)
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, stderr)
	}
	if ExitCode(err) != 0 {
		t.Fatalf("ExitCode = %d, want 0", ExitCode(err))
	}
	for _, name := range []string{
		"fuse_device",
		"squashfuse_ll",
		"fuse_overlayfs",
		"fusermount3",
		"mksquashfs",
		"git",
		"squashfuse_subdir",
	} {
		if !strings.Contains(stdout, name+"\t") {
			t.Fatalf("stdout missing %s\n%s", name, stdout)
		}
	}
}

func TestDoctorFailingProbeStillExitsZero(t *testing.T) {
	withWorkspace(t)
	restoreDoctor(t, func(context.Context) ([]doctor.Result, error) {
		return []doctor.Result{{Name: "git", Status: "fail", Detail: "missing"}}, nil
	})
	stdout, stderr, err := runDoctorCmd(context.Background(), t)
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, stderr)
	}
	if stdout != "git\tfail\tmissing\n" {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestDoctorCancelledContextExitsOne(t *testing.T) {
	withWorkspace(t)
	restoreDoctor(t, func(context.Context) ([]doctor.Result, error) {
		return nil, context.Canceled
	})
	_, stderr, err := runDoctorCmd(context.Background(), t)
	if ExitCode(err) != 1 {
		t.Fatalf("ExitCode(%v) = %d, want 1\n%s", err, ExitCode(err), stderr)
	}
	if strings.Contains(stderr, "Usage:") {
		t.Fatalf("cancelled doctor printed usage\n%s", stderr)
	}
}

func restoreDoctor(t *testing.T, fn func(context.Context) ([]doctor.Result, error)) {
	t.Helper()
	previous := runDoctor
	runDoctor = fn
	t.Cleanup(func() { runDoctor = previous })
}

func runDoctorCmd(ctx context.Context, t *testing.T) (stdout, stderr string, err error) {
	t.Helper()
	cmd := NewRootCmd()
	cmd.SetContext(ctx)
	var out, errBuf strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	cmd.SetArgs([]string{"doctor"})
	err = cmd.Execute()
	return out.String(), errBuf.String(), err
}
