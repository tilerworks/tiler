package workspace

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestFuseMountErrorIdentifiesTargetAndPhase(t *testing.T) {
	for _, phase := range []string{"image layer", "merged view"} {
		cause := errors.New("fusermount3: mount failed: Operation not permitted")
		err := fuseMountError("/view", phase, cause)
		for _, want := range []string{phase, `"/view"`, "CAP_SYS_ADMIN"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%v missing %q", err, want)
			}
		}
		if !errors.Is(err, cause) {
			t.Fatalf("lost cause: %v", err)
		}
	}
}

func TestMountTimeoutReportsReadinessTargetAndAdvice(t *testing.T) {
	err := waitLive("/view", hooks{live: func(string) bool { return false }, sleep: func(time.Duration) {}})
	for _, want := range []string{"FUSE mount readiness", `"/view"`, "timed out after", "FUSE daemon output", "process exited"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("timeout %q missing %q", err, want)
		}
	}
}
