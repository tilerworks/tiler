//go:build linux

package image

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildFullVersionPublishesSingleAndSplitHistory(t *testing.T) {
	cfg := buildConfig(t)
	cfg.ImageFormat, cfg.ImageCompression = FormatErofs, "lz4"
	runner := &fakeRunner{body: []byte("first-version")}
	req := FullRequest{Config: cfg, Name: "alice/app", SrcDir: sourceDir(t), Builder: "test",
		Now: fixedNow, Run: runner.run, MountSmoke: skipSmoke, Description: "Base\n\nDetails", DescriptionSet: true}
	first, err := BuildFullVersion(req)
	if err != nil || first.Format != (Format{Type: FormatErofs, Compression: "lz4"}) || first.Description != req.Description {
		t.Fatalf("single version: %#v, %v", first, err)
	}
	repo, _ := OpenRepo(cfg.ImagesDir, req.Name)
	firstPath, _ := repo.ObjectPayloadPath(first)
	assertBytes(t, firstPath, runner.body)

	req.Format, req.Compression, req.BlockSize = FormatSquashfs, "gzip", 4
	req.Description, runner.body = "Split", []byte("second-version")
	second, err := BuildFullVersion(req)
	if err != nil || len(second.Blocks) < 2 || second.Format != (Format{Type: FormatSquashfs, Compression: "gzip"}) {
		t.Fatalf("split version: %#v, %v", second, err)
	}
	assertBytes(t, firstPath, []byte("first-version"))
	manifest, _, err := loadVersionMeta(repo)
	if err != nil || len(manifest.Images) != 2 {
		t.Fatalf("retained history: %#v, %v", manifest, err)
	}
	if strings.HasPrefix(runner.calls[0][len(runner.calls[0])-1], cfg.ImagesDir) {
		t.Fatal("packer staging was inside images_dir")
	}
}

func TestBuildFullVersionIsIdempotentAndCleansFailures(t *testing.T) {
	cfg, runner := buildConfig(t), &fakeRunner{body: []byte("same")}
	req := FullRequest{Config: cfg, Name: "alice/app", SrcDir: sourceDir(t), Builder: "test",
		Now: fixedNow, Run: runner.run, MountSmoke: skipSmoke}
	first, err := BuildFullVersion(req)
	if err != nil {
		t.Fatal(err)
	}
	again, err := BuildFullVersion(req)
	if err != nil || again.ID != first.ID || !again.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("idempotent build: %#v, %v", again, err)
	}
	req.Description, req.DescriptionSet = "conflict", true
	if _, err := BuildFullVersion(req); err == nil {
		t.Fatal("accepted description conflict")
	}
	req.Name, req.Description, req.DescriptionSet = "other/app", "", false
	runner.err = errors.New("packer failed")
	if _, err := BuildFullVersion(req); err == nil {
		t.Fatal("accepted packer failure")
	}
	entries, err := os.ReadDir(cfg.CacheDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("staging not cleaned: %v, %v", entries, err)
	}
	req.Name, runner.err = "unsafe/app", nil
	req.SrcDir = filepath.Dir(cfg.ImagesDir)
	if _, err := BuildFullVersion(req); err == nil {
		t.Fatal("accepted source covering images_dir")
	}
}
