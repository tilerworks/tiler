package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/image"
)

// Fixed golden identifiers: every id is the first 16 hex digits of
// its sha256, as the library requires.
const (
	goldenActiveID  = "0123456789abcdef"
	goldenActiveSum = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	goldenChainID   = "1111111111111111"
	goldenChainSum  = "1111111111111111111111111111111111111111111111111111111111111111"
	goldenArchiveID = "2222222222222222"
	goldenHoldID    = "3333333333333333"
	goldenBobID     = "4444444444444444"
	goldenBobSum    = "4444444444444444444444444444444444444444444444444444444444444444"
)

func TestImagesListJSONGoldens(t *testing.T) {
	home, _ := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	withBuildSeams(t, nil)
	writeGoldenLibrary(t, imagesDir)

	t.Run("all repositories", func(t *testing.T) {
		stdout := mustListJSON(t, lib, "--json")
		assertGolden(t, "images_ls", []byte(stdout))
		doc := parseImagesDocument(t, stdout)
		if len(doc.Images) != 2 {
			t.Fatalf("images = %d, want 2 (the inactive repository is skipped)",
				len(doc.Images))
		}
		alice, bob := doc.Images[0], doc.Images[1]
		if alice.Name != "alice/app" || bob.Name != "bob/tool" {
			t.Fatalf("names = %s, %s, want alice/app, bob/tool", alice.Name, bob.Name)
		}
		if len(alice.Archives) != 0 || len(alice.Holds) != 0 {
			t.Fatal("archives and holds must stay empty without the flag")
		}
		if alice.Active.ID != goldenActiveID || len(alice.Active.Chain) != 1 ||
			alice.Active.Chain[0].ID != goldenChainID {
			t.Fatalf("alice active = %+v, want the fixture image with one chain entry",
				alice.Active)
		}
	})

	t.Run("all repositories with archives", func(t *testing.T) {
		stdout := mustListJSON(t, lib, "--archives", "--json")
		assertGolden(t, "images_ls_archives", []byte(stdout))
		doc := parseImagesDocument(t, stdout)
		alice := doc.Images[0]
		if len(alice.Archives) != 1 || alice.Archives[0] != goldenArchiveID {
			t.Fatalf("archives = %v, want [%s]", alice.Archives, goldenArchiveID)
		}
		if len(alice.Holds) != 1 || alice.Holds[0] != goldenHoldID {
			t.Fatalf("holds = %v, want [%s]", alice.Holds, goldenHoldID)
		}
	})

	t.Run("list rejects a repository name", func(t *testing.T) {
		_, _, _, err := runCmd(t, append(lib, "images", "ls", "alice/app", "--json")...)
		if ExitCode(err) != 2 {
			t.Fatalf("ExitCode(%v) = %d, want 2", err, ExitCode(err))
		}
	})
}

func TestImagesShowJSONGolden(t *testing.T) {
	home, _ := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	withBuildSeams(t, nil)
	writeGoldenLibrary(t, imagesDir)

	stdout := mustShowJSON(t, lib, "alice/app")
	assertGolden(t, "images_show", []byte(stdout))
	var view imageJSON
	if err := json.Unmarshal([]byte(stdout), &view); err != nil {
		t.Fatalf("images show --json is not parseable: %v\n%s", err, stdout)
	}
	if view.Name != "alice/app" || view.Active.ID != goldenActiveID {
		t.Fatalf("show = %+v", view)
	}
	if len(view.Archives) != 1 || view.Archives[0] != goldenArchiveID {
		t.Fatalf("archives = %v, want [%s]", view.Archives, goldenArchiveID)
	}
	if len(view.Holds) != 1 || view.Holds[0] != goldenHoldID {
		t.Fatalf("holds = %v, want [%s]", view.Holds, goldenHoldID)
	}
}

func TestImagesShowJSONKeepsErrorSemantics(t *testing.T) {
	home, _ := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	withBuildSeams(t, nil)
	writeGoldenLibrary(t, imagesDir)

	tests := []struct {
		name string
		args []string
		want int
	}{
		{name: "bad name is a usage error", args: []string{"nope", "--json"}, want: 2},
		{name: "too many arguments is a usage error", args: []string{"a", "b", "--json"}, want: 2},
		{name: "missing name is a usage error", args: []string{"--json"}, want: 2},
		{name: "repository without an active image fails", args: []string{"carol/old", "--json"}, want: 1},
		{name: "unknown repository fails", args: []string{"bob/missing", "--json"}, want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, err := runCmd(t, append(lib, append([]string{"images", "show"}, tt.args...)...)...)
			if ExitCode(err) != tt.want {
				t.Fatalf("ExitCode(%v) = %d, want %d", err, ExitCode(err), tt.want)
			}
		})
	}
}

func TestImagesListHumanRecord(t *testing.T) {
	home, _ := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	withBuildSeams(t, nil)
	writeGoldenLibrary(t, imagesDir)

	want := "" +
		"NAME       ID                CREATED     SIZE   CHAIN  FORM    ARCH  HOLD\n" +
		"alice/app  " + goldenActiveID + "  2026-03-04  2 KiB  1      single  1     1\n" +
		"bob/tool   " + goldenBobID + "  2026-03-04  4 KiB  0      single  0     0\n"
	if out := mustList(t, lib); out != want {
		t.Fatalf("ls all:\n%s\nwant:\n%s", out, want)
	}
	want = "alice/app\n" +
		"  id          " + goldenActiveID + "\n" +
		"  created     2026-03-04T05:06:07Z\n" +
		"  size        2048 (2 KiB)\n" +
		"  sha256      " + goldenActiveSum + "\n" +
		"  form        single\n" +
		"  builder     tiler golden\n" +
		"  chain       1\n" +
		"    " + goldenChainID + "  parent=" + goldenActiveID + "  1 KiB  2026-03-04T04:06:07Z\n" +
		"  archives    1\n" +
		"    " + goldenArchiveID + "\n" +
		"  holds       1\n" +
		"    " + goldenHoldID + "\n"
	if out := mustShow(t, lib, "alice/app"); out != want {
		t.Fatalf("show:\n%s\nwant:\n%s", out, want)
	}
}

// writeGoldenLibrary lays out a deterministic image library:
// alice/app with an active image, one chain layer, one archived and
// one held chain; bob/tool with a plain active image; carol/old
// without an active image.
func writeGoldenLibrary(t *testing.T, imagesDir string) {
	t.Helper()
	createdAt := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	format := image.Format{Type: image.FormatSquashfs, Compression: image.CompressionZstd}
	alice := image.Meta{
		ID: goldenActiveID, Owner: "alice", Repo: "app", Version: "v3",
		Kind: image.KindIncremental, Format: format,
		Size: 2048, SHA256: goldenActiveSum, MtimeUnix: 1772600767,
		CreatedAt: createdAt, Builder: "tiler golden",
		Chain: []image.ChainEntry{{
			ID: goldenChainID, Parent: goldenActiveID, Size: 1024,
			SHA256: goldenChainSum, MtimeUnix: 1772600000,
			CreatedAt: createdAt.Add(-time.Hour),
		}},
	}
	aliceDir := filepath.Join(imagesDir, "alice", "app")
	storeGoldenRepo(t, aliceDir, alice)
	writeGoldenChainDir(t, aliceDir, "archives", goldenArchiveID)
	writeGoldenChainDir(t, aliceDir, "hold", goldenHoldID)
	bob := image.Meta{
		ID: goldenBobID, Owner: "bob", Repo: "tool", Version: "v1",
		Kind: image.KindFull, Format: format,
		Size: 4096, SHA256: goldenBobSum, MtimeUnix: 1772600767,
		CreatedAt: createdAt.Add(2 * time.Hour), Builder: "tiler golden",
	}
	storeGoldenRepo(t, filepath.Join(imagesDir, "bob", "tool"), bob)
	if err := os.MkdirAll(filepath.Join(imagesDir, "carol", "old"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// storeGoldenRepo writes one repository directory with the fixed
// metadata and a placeholder image file.
func storeGoldenRepo(t *testing.T, dir string, meta image.Meta) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := image.Store(filepath.Join(dir, "meta.yaml"), meta); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "image.sqsh"), []byte("golden"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeGoldenChainDir(t *testing.T, repoDir, sub, id string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(repoDir, sub, id), 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustListJSON(t *testing.T, lib []string, args ...string) string {
	t.Helper()
	full := append([]string{"images", "ls"}, args...)
	stdout, stderr, _, err := runCmd(t, append(lib, full...)...)
	if err != nil {
		t.Fatalf("images ls %v: %v\n%s", args, err, stderr)
	}
	return stdout
}

func mustShowJSON(t *testing.T, lib []string, args ...string) string {
	t.Helper()
	full := append([]string{"images", "show"}, args...)
	full = append(full, "--json")
	stdout, stderr, _, err := runCmd(t, append(lib, full...)...)
	if err != nil {
		t.Fatalf("images show %v: %v\n%s", args, err, stderr)
	}
	return stdout
}

func parseImagesDocument(t *testing.T, stdout string) imagesDocument {
	t.Helper()
	var doc imagesDocument
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("images ls --json is not parseable: %v\n%s", err, stdout)
	}
	return doc
}

func TestImagesListSplitJSONGolden(t *testing.T) {
	home, _ := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	withBuildSeams(t, nil)
	writeBlocksFixture(t, imagesDir, "alice/data", []string{"block-one", "b2"}, true)
	writeMembersFixture(t, imagesDir, "bob/forest", []fixtureMember{
		{name: "docs", body: "docs-bytes"},
		{name: "src", body: "src-bytes-longer"},
	})

	stdout := mustListJSON(t, lib, "--json")
	assertGolden(t, "images_ls_split", []byte(stdout))
	doc := parseImagesDocument(t, stdout)
	if len(doc.Images) != 2 {
		t.Fatalf("images = %+v, want the two split repositories", doc.Images)
	}
	blocks, members := doc.Images[0].Active, doc.Images[1].Active
	if blocks.Form != "blocks" || len(blocks.Members) != 0 {
		t.Errorf("blocks active = %+v", blocks)
	}
	if members.Form != "members" || len(members.Members) != 2 {
		t.Fatalf("members active = %+v", members)
	}
	first := members.Members[0]
	if first.Name != "docs" || first.Image != "members/docs.sqsh" ||
		first.Size != int64(len("docs-bytes")) || len(first.SHA256) != 64 {
		t.Errorf("member entry = %+v", first)
	}
}
