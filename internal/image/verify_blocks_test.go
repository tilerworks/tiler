package image

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVerifyBlockPayloadWithoutMaterializing(t *testing.T) {
	dir, node := stagedSplitVersion(t, "logical image bytes", 5)
	var blocks []BlockFile
	for _, b := range node.Blocks {
		blocks = append(blocks, BlockFile{Name: b.Name, Path: filepath.Join(dir, b.Name), Size: b.Size, SHA256: b.SHA256})
	}
	if err := VerifyBlockPayload(blocks, node.SHA256, node.Size); err != nil {
		t.Fatal(err)
	}
	if err := VerifyBlockPayload(blocks, node.SHA256, node.Size+1); err == nil {
		t.Fatal("accepted the wrong logical size")
	}
	if err := VerifyBlockPayload(blocks, versionNode("other").SHA256, node.Size); err == nil {
		t.Fatal("accepted the wrong logical digest")
	}
	if err := os.WriteFile(blocks[0].Path, []byte("wrong"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := VerifyBlockPayload(blocks, node.SHA256, node.Size); err == nil {
		t.Fatal("accepted a corrupted block")
	}
}
