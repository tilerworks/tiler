package image

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestPlanImportSourceAliasesAndSidecar(t *testing.T) {
	withLinuxLinkPolicy(t)
	images := t.TempDir()
	nas := t.TempDir()
	payload := []byte("vendor-payload")
	src := writeFakeSquashfs(t, nas, payload)
	sub := filepath.Join(nas, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	lexical := filepath.Join(sub, "..", filepath.Base(src))
	link := filepath.Join(nas, "alias.sqsh")
	if err := os.Symlink(src, link); err != nil {
		t.Fatal(err)
	}
	sum := strings.Repeat("ab", 32)
	if err := os.WriteFile(link+".sha256", []byte(strings.ToUpper(sum)+" *alias.sqsh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	direct := planImportOK(t, ImportRequest{Mode: ImportLink, ImagesDir: images, Name: "vendor/app", Source: lexical})
	viaLink := planImportOK(t, ImportRequest{Mode: ImportLink, ImagesDir: images, Name: "vendor/app", Source: link})
	copied := planImportOK(t, ImportRequest{Mode: ImportCopy, ImagesDir: images, Name: "vendor/app", Source: link})
	wantLoc, err := filepath.EvalSymlinks(src)
	if err != nil {
		t.Fatal(err)
	}
	if direct.Location != wantLoc || viaLink.Location != wantLoc || copied.Location != wantLoc {
		t.Fatalf("locations = %s %s %s, want %s", direct.Location, viaLink.Location, copied.Location, wantLoc)
	}
	content := sha256.Sum256(mustRead(t, src))
	if !IsUnknownSHA256(direct.SHA256) || hex.EncodeToString(content[:]) == direct.SHA256 {
		t.Fatalf("missing sidecar digest = %s", direct.SHA256)
	}
	if viaLink.Given == viaLink.Location || viaLink.SHA256 != sum || IsUnknownSHA256(viaLink.SHA256) {
		t.Fatalf("symlink sidecar plan = %+v", viaLink)
	}
	if copied.SHA256 != "" || copied.Format != viaLink.Format || copied.Size != viaLink.Size {
		t.Fatalf("copy plan = %+v", copied)
	}
	info, err := os.Stat(wantLoc)
	if err != nil || viaLink.Size != info.Size() || viaLink.MtimeUnix != info.ModTime().Unix() {
		t.Fatalf("stat = %+v plan = %+v err %v", info, viaLink, err)
	}
	if viaLink.Format.Type != FormatSquashfs || viaLink.Format.Compression != CompressionZstd {
		t.Fatalf("format = %+v", viaLink.Format)
	}
	if _, err := os.Stat(viaLink.Repo.Dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("planning created target: %v", err)
	}
	zeros := filepath.Join(nas, "zeros.sqsh")
	if err := os.Symlink(src, zeros); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(zeros+".sha256", []byte(UnknownSHA256+"  zeros.sqsh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	marked := planImportOK(t, ImportRequest{Mode: ImportLink, ImagesDir: images, Name: "vendor/app", Source: zeros})
	if !IsUnknownSHA256(marked.SHA256) {
		t.Fatalf("explicit zero = %s", marked.SHA256)
	}
}

func TestPlanImportSourceLeavesReadOnlyTreeUnchanged(t *testing.T) {
	images := t.TempDir()
	src := filepath.Join(images, "other", "lib")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	writeImportLibrary(t, src)
	if err := os.Chmod(src, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(src, 0o755) })
	before := importStamp(t, images)
	for _, mode := range []ImportMode{ImportLink, ImportCopy} {
		plan := planImportOK(t, ImportRequest{
			Mode: mode, ImagesDir: images, Name: "vendor/app", Source: src, Hash: "all",
		})
		if plan.Kind != SourceDir || plan.SHA256 != "" || plan.Name != "vendor/app" {
			t.Fatalf("dir plan = %+v", plan)
		}
		meta, err := os.Stat(filepath.Join(plan.Location, fileMeta))
		if err != nil || plan.Size != meta.Size() || plan.MtimeUnix != meta.ModTime().Unix() {
			t.Fatalf("meta stat = %+v plan size %d err %v", meta, plan.Size, err)
		}
		if _, err := os.Stat(plan.Repo.Dir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("planning created target: %v", err)
		}
	}
	if after := importStamp(t, images); after != before {
		t.Fatalf("source tree changed\n%s\n%s", before, after)
	}
}

func TestPlanImportSourceRejectsInvalidSources(t *testing.T) {
	withLinuxLinkPolicy(t)
	images := t.TempDir()
	nas := t.TempDir()
	good := writeFakeSquashfs(t, nas, []byte("ok"))
	req := func(source string) ImportRequest {
		return ImportRequest{Mode: ImportLink, ImagesDir: images, Name: "vendor/app", Source: source}
	}
	ref := filepath.Join(nas, "ref.sqsh")
	if err := os.WriteFile(ref, []byte(good+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cycleA, cycleB := filepath.Join(nas, "cycle-a.sqsh"), filepath.Join(nas, "cycle-b.sqsh")
	if err := os.WriteFile(cycleA, []byte(cycleB+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cycleB, []byte(cycleA+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	danglingRef := filepath.Join(nas, "dangling-ref.sqsh")
	if err := os.WriteFile(danglingRef, []byte(filepath.Join(nas, "missing.sqsh")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pointedDir := filepath.Join(nas, "pointed-dir")
	if err := os.Mkdir(pointedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dirRef := filepath.Join(nas, "dir-ref.sqsh")
	if err := os.WriteFile(dirRef, []byte(pointedDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	danglingLink := filepath.Join(nas, "dangling.sqsh")
	if err := os.Symlink(filepath.Join(nas, "missing.sqsh"), danglingLink); err != nil {
		t.Fatal(err)
	}
	block := filepath.Join(nas, "blk.000")
	if err := os.WriteFile(block, []byte("hsqsfragment"), 0o644); err != nil {
		t.Fatal(err)
	}
	emptyDir := filepath.Join(nas, "empty-dir")
	if err := os.Mkdir(emptyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	oldDir := filepath.Join(nas, "old-meta")
	if err := os.Mkdir(oldDir, 0o755); err != nil {
		t.Fatal(err)
	}
	oldMeta := "version: \"2.0\"\nname: other/lib\nimages: []\n"
	if err := os.WriteFile(filepath.Join(oldDir, fileMeta), []byte(oldMeta), 0o644); err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(nas, "plain.txt")
	if err := os.WriteFile(plain, []byte("not-a-container"), 0o644); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(images, "vendor", "app")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	self := writeFakeSquashfs(t, inside, []byte("self"))
	alias := filepath.Join(nas, "self-alias.sqsh")
	if err := os.Symlink(self, alias); err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	parentImages := filepath.Join(parent, "images")
	if err := os.Mkdir(parentImages, 0o755); err != nil {
		t.Fatal(err)
	}
	writeImportLibrary(t, parent)

	cases := []struct {
		name string
		req  ImportRequest
		want string
	}{
		{name: "empty source", req: ImportRequest{Mode: ImportLink, ImagesDir: images, Name: "vendor/app"}, want: "required"},
		{name: "bad mode", req: ImportRequest{Mode: "both", ImagesDir: images, Name: "vendor/app", Source: good}, want: "link or copy"},
		{name: "bad name", req: ImportRequest{Mode: ImportCopy, ImagesDir: images, Name: "../x", Source: good}, want: "invalid image name"},
		{name: "hash on file", req: ImportRequest{Mode: ImportLink, ImagesDir: images, Name: "vendor/app", Source: good, Hash: "all"}, want: "image directory"},
		{name: "missing", req: req(filepath.Join(nas, "missing.sqsh")), want: "no such file"},
		{name: "dangling symlink", req: req(danglingLink), want: "no such file"},
		{name: "location file", req: req(ref), want: "location file"},
		{name: "location cycle", req: req(cycleA), want: "cycle"},
		{name: "dangling location", req: req(danglingRef), want: "dangling location"},
		{name: "location names directory", req: req(dirRef), want: "points at"},
		{name: "block fragment", req: req(block), want: "block fragment"},
		{name: "plain file", req: req(plain), want: "not detected"},
		{name: "directory without metadata", req: req(emptyDir), want: "metadata"},
		{name: "unsupported metadata", req: req(oldDir), want: "unsupported metadata"},
		{name: "file inside target", req: req(self), want: "overlaps"},
		{name: "symlink into target", req: req(alias), want: "overlaps"},
		{name: "directory equals target", req: req(inside), want: "overlaps"},
		{
			name: "directory contains target",
			req:  ImportRequest{Mode: ImportCopy, ImagesDir: parentImages, Name: "vendor/app", Source: parent},
			want: "overlaps",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, err := PlanImportSource(tt.req)
			if err == nil || !strings.Contains(err.Error(), tt.want) ||
				!strings.Contains(err.Error(), string(tt.req.Mode)) ||
				!strings.Contains(err.Error(), tt.req.Name) {
				t.Fatalf("err = %v", err)
			}
		})
	}
	if err := os.Mkdir(good+".sha256", 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanImportSource(req(good)); !errors.Is(err, ErrChecksumSidecar) {
		t.Fatalf("link sidecar: %v", err)
	}
	copied := planImportOK(t, ImportRequest{Mode: ImportCopy, ImagesDir: images, Name: "vendor/app", Source: good})
	if copied.SHA256 != "" {
		t.Fatalf("copy hashed or read sidecar: %s", copied.SHA256)
	}
}

func TestPlanImportSourceRejectsFilesystemRoot(t *testing.T) {
	images := t.TempDir()
	repoDir := filepath.Join(images, "vendor", "app")
	meta := string(filepath.Separator) + fileMeta
	before, beforeErr := os.Lstat(meta)
	root := ImportRequest{Mode: ImportLink, ImagesDir: images, Name: "vendor/app", Source: "/"}
	_, err := PlanImportSource(root)
	if err == nil || !strings.Contains(err.Error(), "overlaps") {
		t.Fatalf("root source: %v", err)
	}
	link := filepath.Join(t.TempDir(), "root-link")
	if err := os.Symlink("/", link); err != nil {
		t.Fatal(err)
	}
	root.Source = link
	_, err = PlanImportSource(root)
	if err == nil || !strings.Contains(err.Error(), "overlaps") {
		t.Fatalf("symlink to root: %v", err)
	}
	if err := validateImportLocation("/", repoDir); err == nil || !strings.Contains(err.Error(), "overlaps") {
		t.Fatalf("validate root: %v", err)
	}
	after, afterErr := os.Lstat(meta)
	if os.IsNotExist(beforeErr) {
		if !os.IsNotExist(afterErr) {
			t.Fatal("planning created /meta.yaml")
		}
		return
	}
	if beforeErr != nil || afterErr != nil || !before.ModTime().Equal(after.ModTime()) || before.Size() != after.Size() {
		t.Fatalf("planning changed /meta.yaml: before %v after %v", beforeErr, afterErr)
	}
}

func TestImportLocationRejectsUnsafePositionText(t *testing.T) {
	if err := validateImportLocation("ok\npath", "/tmp/unused"); err == nil {
		t.Fatal("newline location accepted")
	}
	if err := validateImportLocation("ok\x00path", "/tmp/unused"); err == nil {
		t.Fatal("nul location accepted")
	}
	long := "/" + strings.Repeat("a", pathRefMaxSize)
	if err := validateImportLocation(long, "/tmp/unused"); err == nil {
		t.Fatal("overlong location accepted")
	}
}

func planImportOK(t *testing.T, req ImportRequest) SourcePlan {
	t.Helper()
	plan, err := PlanImportSource(req)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func writeImportLibrary(t *testing.T, dir string) {
	t.Helper()
	id := strings.Repeat("ab", 32)
	body, err := EncodeVersionManifest(VersionManifest{
		Version: VersionManifestVersion,
		Name:    "other/lib",
		Images: []ImageMeta{{
			ID: id, Kind: KindFull, SHA256: id, Size: 4,
			Format:    Format{Type: FormatSquashfs, Compression: CompressionZstd},
			MtimeUnix: 1, CreatedAt: time.Unix(1, 0).UTC(), Builder: "vendor",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, fileMeta), body, 0o644); err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(dir, ImageFile)
	if err := os.WriteFile(payload, []byte("hsqs-unread"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(payload, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(payload, 0o644) })
}

func importStamp(t *testing.T, root string) string {
	t.Helper()
	var lines []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		lines = append(lines, fmt.Sprintf("%s %d %d %o", rel, info.Size(), info.ModTime().UnixNano(), info.Mode()))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(lines)
	return strings.Join(lines, "\n")
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestPlanImportSourcePayloadBytesStayPut(t *testing.T) {
	withLinuxLinkPolicy(t)
	nas := t.TempDir()
	payload := []byte("hsqs-stable")
	src := writeFakeSquashfs(t, nas, payload)
	before := mustRead(t, src)
	planImportOK(t, ImportRequest{Mode: ImportLink, ImagesDir: t.TempDir(), Name: "vendor/app", Source: src})
	if after := mustRead(t, src); !bytes.Equal(before, after) {
		t.Fatal("payload bytes changed")
	}
}
