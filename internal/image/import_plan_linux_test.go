//go:build linux

package image

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestPlanImportSourceLinkReadIsBounded(t *testing.T) {
	withLinuxLinkPolicy(t)
	const payloadSize = 2 << 20
	nas := t.TempDir()
	images := t.TempDir()
	body := make([]byte, payloadSize)
	copy(body, []byte("hsqs"))
	src := filepath.Join(nas, "large.sqsh")
	if err := os.WriteFile(src, body, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := strings.Repeat("cd", 32)
	if err := os.WriteFile(src+".sha256", []byte(sum+"  large.sqsh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	linked := planWithinReadBound(t, ImportRequest{
		Mode: ImportLink, ImagesDir: images, Name: "vendor/app", Source: src,
	}, payloadSize)
	if linked.SHA256 != sum || linked.Size != payloadSize {
		t.Fatalf("link plan = %+v", linked)
	}
	copied := planWithinReadBound(t, ImportRequest{
		Mode: ImportCopy, ImagesDir: images, Name: "vendor/app", Source: src,
	}, payloadSize)
	if copied.SHA256 != "" {
		t.Fatalf("copy plan hashed payload: %s", copied.SHA256)
	}
	dir := filepath.Join(nas, "library")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeImportLibrary(t, dir)
	large := filepath.Join(dir, "unread.sqsh")
	if err := os.WriteFile(large, body, 0o644); err != nil {
		t.Fatal(err)
	}
	planned := planWithinReadBound(t, ImportRequest{
		Mode: ImportLink, ImagesDir: images, Name: "vendor/app", Source: dir, Hash: "ab",
	}, payloadSize)
	if planned.Kind != SourceDir {
		t.Fatalf("dir plan = %+v", planned)
	}
}

func planWithinReadBound(t *testing.T, req ImportRequest, payload int64) SourcePlan {
	t.Helper()
	before := selfRChar(t)
	plan := planImportOK(t, req)
	delta := selfRChar(t) - before
	const slack = 64 << 10
	if delta > slack || delta >= uint64(payload)/2 {
		t.Fatalf("read %d bytes while planning a %d byte payload", delta, payload)
	}
	return plan
}

func selfRChar(t *testing.T) uint64 {
	t.Helper()
	data, err := os.ReadFile("/proc/self/io")
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		rest, ok := strings.CutPrefix(line, "rchar: ")
		if !ok {
			continue
		}
		n, err := strconv.ParseUint(strings.TrimSpace(rest), 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	t.Fatal("rchar missing")
	return 0
}
