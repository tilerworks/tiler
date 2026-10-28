package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jiangxin/tiler/internal/image"
)

const goldenActiveSum = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func manifestRoot(m image.Manifest) image.ImageMeta {
	root, _ := m.Full()
	return root
}

func manifestRootID(m image.Manifest) string { return manifestRoot(m).ID }

func writeKolyFile(t *testing.T, path string) {
	t.Helper()
	body := make([]byte, 512)
	copy(body, []byte("koly"))
	writeTestFile(t, path, string(body))
}

func containerBody(formatType, tag string) []byte {
	if formatType == image.FormatDMG {
		body := make([]byte, 512)
		copy(body, []byte("koly"))
		copy(body[4:], []byte(tag))
		return body
	}
	return []byte("hsqs-" + tag)
}

func writeImmutablePayload(t *testing.T, repo image.Repo, node image.ImageMeta, body []byte) string {
	t.Helper()
	path, err := repo.ObjectPayloadPath(node)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
