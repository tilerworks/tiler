//go:build !darwin && !linux

package benchmark

import (
	"strings"
	"testing"
)

func TestAttachOnOtherPlatformReturnsError(t *testing.T) {
	rec := &argvRec{}
	for _, typ := range []string{"dmg", "squashfs", "erofs"} {
		t.Run(typ, func(t *testing.T) {
			_, err := Attach(rec.command, Host{}, typ, packImage, packPoint)
			if err == nil || !strings.Contains(err.Error(), "not available") {
				t.Fatalf("Attach error = %v", err)
			}
		})
	}
	if len(rec.calls) != 0 {
		t.Fatalf("calls = %v", rec.calls)
	}
	err := Detach(rec.command, Mount{Tool: "mount"}, packPoint)
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("Detach error = %v", err)
	}
}
