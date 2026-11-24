//go:build linux

package checkpoint

import (
	"testing"

	"github.com/jiangxin/tiler/internal/mountinfo"
)

func TestMountTreeChecksRejectNestedMounts(t *testing.T) {
	mounts := []mountinfo.Mount{{Point: "/tmp/example/nested"}}
	if err := checkTreeMountPoints("/tmp/example", mounts); err == nil {
		t.Fatal("accepted nested mount")
	}
	if err := checkTreeMountPoints("/tmp/other", mounts); err != nil {
		t.Fatalf("unrelated mount: %v", err)
	}
}
