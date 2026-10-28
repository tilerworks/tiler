package image

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDMGVersionBakePinsBaseAndReusesPostConvertContent(t *testing.T) {
	cfg := buildConfig(t)
	repo, _ := OpenRepo(cfg.ImagesDir, "alice/app")
	base, newer := versionNode("base"), versionNode("newer")
	base.Format, newer.Format = Format{Type: FormatDMG, Compression: "ulfo"}, Format{Type: FormatDMG, Compression: "ulmo"}
	for _, node := range []ImageMeta{base, newer} {
		if _, err := PublishFullVersion(cfg.ImagesDir, repo.Name(), writeSource(t, t.TempDir(), []byte(node.Description)), node, true); err != nil {
			t.Fatal(err)
		}
	}
	runner := &dmgRunner{body: []byte("baked"), afterConvertShadow: []byte("post-convert")}
	req := DMGRequest{Config: cfg, Name: repo.Name(), MountedID: base.ID, Shadow: writeShadow(t, cfg.SessionsDir, "dev", 1, []byte("before")),
		Builder: "test", Now: fixedNow, Run: runner.run, Description: "Baked version", DescriptionSet: true}
	before, _ := versionShadowContent(req)
	first, err := BuildDMGVersion(req)
	post, _ := versionShadowContent(req)
	if err != nil || first.ContentSHA256 != post || post == before || first.Format != base.Format {
		t.Fatalf("bake metadata: %#v, %v", first, err)
	}
	basePath := filepath.Join(repo.Dir, dirObjects, base.ID, ImageDMG)
	if !strings.Contains(strings.Join(runner.calls[0], " "), basePath) {
		t.Fatalf("convert did not use pinned base: %q", runner.calls)
	}
	got, err := BuildDMGVersion(req)
	if err != nil || got.ID != first.ID || got.Description != first.Description || !got.CreatedAt.Equal(first.CreatedAt) || len(runner.calls) != 1 {
		t.Fatalf("repeat bake not idle: %#v, %v", got, err)
	}
	req.Description = "Conflict"
	if _, err := BuildDMGVersion(req); err == nil || len(runner.calls) != 1 {
		t.Fatal("accepted description conflict")
	}
	req.MountedID, req.DescriptionSet, runner.body = newer.ID, false, []byte("other-bake")
	if other, err := BuildDMGVersion(req); err != nil || other.ID == first.ID || other.ContentSHA256 == first.ContentSHA256 || other.Format != newer.Format {
		t.Fatalf("different base incorrectly reused: %#v, %v", other, err)
	}
	assertBytes(t, basePath, []byte("base"))
	assertBytes(t, filepath.Join(repo.Dir, dirObjects, newer.ID, ImageDMG), []byte("newer"))
	if _, err := os.Stat(dmgArgOut(runner.calls[0][1:])); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("bake staging was retained")
	}
	previous, _ := os.ReadFile(repo.MetaPath())
	for _, failure := range []string{"pin", "missing pin", "shadow", "compression", "description", "tool"} {
		t.Run(failure, func(t *testing.T) {
			bad := req
			switch failure {
			case "pin":
				bad.MountedID = ""
			case "missing pin":
				bad.MountedID = strings.Repeat("f", 64)
			case "shadow":
				bad.Shadow += ".missing"
			case "compression":
				bad.Compression = "invalid"
			case "description":
				bad.Description = "\ninvalid"
			case "tool":
				bad.Compression, runner.err = "ulfo", errors.New("convert failed")
			}
			if _, err := BuildDMGVersion(bad); err == nil {
				t.Fatal("accepted invalid or failed bake")
			}
			current, _ := os.ReadFile(repo.MetaPath())
			if string(current) != string(previous) {
				t.Fatal("failed bake changed history")
			}
		})
	}
}
