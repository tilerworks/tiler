//go:build linux

package image

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStageImportFileDoesNotRereadSource(t *testing.T) {
	withLinuxLinkPolicy(t)
	const payloadSize = 2 << 20
	images := t.TempDir()
	nas := t.TempDir()
	body := make([]byte, payloadSize)
	copy(body, []byte("hsqs"))
	src := filepath.Join(nas, "large.sqsh")
	if err := os.WriteFile(src, body, 0o644); err != nil {
		t.Fatal(err)
	}
	before := selfRChar(t)
	staged := stageImportOK(t, copyReq(images, src), stageCopyHooks{})
	t.Cleanup(staged.Remove)
	delta := selfRChar(t) - before
	const slack = 64 << 10
	if delta < uint64(payloadSize) || delta > uint64(payloadSize)+slack {
		t.Fatalf("read %d bytes while copying a %d byte payload", delta, payloadSize)
	}
	if IsUnknownSHA256(staged.SHA256) || staged.Size != payloadSize {
		t.Fatalf("staged = %+v", staged)
	}
}
