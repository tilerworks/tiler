package image

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDMGVersionBakeConvertsLinkedPayload(t *testing.T) {
	original := linkDarwinHost
	linkDarwinHost = func() bool { return true }
	t.Cleanup(func() { linkDarwinHost = original })
	cfg := buildConfig(t)
	repo, _ := OpenRepo(cfg.ImagesDir, "alice/app")
	external := writeFakeDMG(t, t.TempDir(), []byte("linked"))
	if err := LinkExternal(cfg.ImagesDir, logsFor(cfg.ImagesDir), repo.Name(), external, "test"); err != nil {
		t.Fatal(err)
	}
	manifest, _, err := loadVersionMeta(repo)
	if err != nil || len(manifest.Images) != 1 {
		t.Fatalf("linked manifest: %#v, %v", manifest, err)
	}
	runner := &dmgRunner{body: []byte("baked")}
	_, err = BuildDMGVersion(DMGRequest{Config: cfg, Name: repo.Name(), MountedID: manifest.Images[0].ID,
		Shadow: writeShadow(t, cfg.SessionsDir, "dev", 1, []byte("before")), Builder: "test", Now: fixedNow, Run: runner.run})
	if err != nil {
		t.Fatalf("bake from linked payload: %v", err)
	}
	if !strings.Contains(strings.Join(runner.calls[0], " "), external) {
		t.Fatalf("convert did not use the linked file: %q", runner.calls)
	}
}

func TestDMGVersionBakeConvertsUnknownLinkedDigest(t *testing.T) {
	cfg := buildConfig(t)
	repo, err := OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	external := writeFakeDMG(t, t.TempDir(), []byte("linked"))
	info, err := os.Stat(external)
	if err != nil {
		t.Fatal(err)
	}
	node := ImageMeta{
		ID: UnknownSHA256, Kind: KindFull, SHA256: UnknownSHA256,
		Size: info.Size(), MtimeUnix: info.ModTime().Unix(),
		Format:    Format{Type: FormatDMG, Compression: "ulfo"},
		CreatedAt: fixedNow(), Builder: "import",
	}
	body, err := EncodeVersionManifest(VersionManifest{
		Version: VersionManifestVersion, Name: repo.Name(), Images: []ImageMeta{node},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(repo.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(repo.MetaPath(), body, 0o644); err != nil {
		t.Fatal(err)
	}
	leaf, err := repo.ObjectPayloadPath(node)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(leaf), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writePathRef(external, leaf); err != nil {
		t.Fatal(err)
	}
	runner := &dmgRunner{body: []byte("baked")}
	got, err := BuildDMGVersion(DMGRequest{
		Config: cfg, Name: repo.Name(), MountedID: UnknownSHA256,
		Shadow:  writeShadow(t, cfg.SessionsDir, "dev", 1, []byte("before")),
		Builder: "test", Now: fixedNow, Run: runner.run,
	})
	if err != nil || IsUnknownSHA256(got.ID) {
		t.Fatalf("bake from unknown link: %#v, %v", got, err)
	}
	if !strings.Contains(strings.Join(runner.calls[0], " "), external) {
		t.Fatalf("convert did not use the linked file: %q", runner.calls)
	}
	target, ok, err := readExternalLinkTarget(leaf)
	if err != nil || !ok || target != external {
		t.Fatalf("linked payload = %q ok %v err %v", target, ok, err)
	}
}

func TestDMGVersionBakePinsBaseAndReusesPostConvertContent(t *testing.T) {
	cfg := buildConfig(t)
	repo, _ := OpenRepo(cfg.ImagesDir, "alice/app")
	base, newer := versionNode("base"), versionNode("newer")
	base.Format, newer.Format = Format{Type: FormatDMG, Compression: "ulfo"}, Format{Type: FormatDMG, Compression: "ulmo"}
	for _, node := range []ImageMeta{base, newer} {
		if _, err := PublishFullVersion(cfg.ImagesDir, logsFor(cfg.ImagesDir), repo.Name(), writeSource(t, t.TempDir(), []byte(node.Description)), node, true); err != nil {
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
