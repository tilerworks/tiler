package image

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDMGVersionsReuseRetainedContentAndDescriptions(t *testing.T) {
	cfg, runner := buildConfig(t), &dmgRunner{body: []byte("first")}
	req := DMGRequest{Config: cfg, Name: "alice/app", SrcDir: sourceDir(t), Builder: "test", Now: fixedNow, Run: runner.run,
		Description: "Base\n\nDetails", DescriptionSet: true}
	first, err := BuildDMGVersion(req)
	if err != nil || first.Format != (Format{Type: FormatDMG, Compression: "ulfo"}) || first.ContentSHA256 == "" {
		t.Fatalf("created version: %#v, %v", first, err)
	}
	stage := dmgArgOut(runner.calls[0][1:])
	if _, err := os.Stat(stage); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging was not removed: %v", err)
	}
	if !strings.Contains(strings.Join(runner.calls[0], " "), "-srcfolder "+req.SrcDir) {
		t.Fatalf("wrong create command: %q", runner.calls)
	}
	retained := versionNode("retained")
	retained.Format, retained.ContentSHA256 = first.Format, first.ContentSHA256
	retained.CreatedAt = first.CreatedAt.Add(time.Hour)
	if _, err := PublishFullVersion(cfg.ImagesDir, logsFor(cfg.ImagesDir), req.Name, writeSource(t, t.TempDir(), []byte("retained")), retained, true); err != nil {
		t.Fatal(err)
	}
	req.Description, req.DescriptionSet = "ignored", false
	got, err := BuildDMGVersion(req)
	if err != nil || got.ID != retained.ID || got.Description != retained.Description || !got.CreatedAt.Equal(retained.CreatedAt) || len(runner.calls) != 1 {
		t.Fatalf("retained content not reused: %#v, %v", got, err)
	}
	req.DescriptionSet = true
	if _, err := BuildDMGVersion(req); err == nil || len(runner.calls) != 1 {
		t.Fatal("explicit description conflict did not stop before packing")
	}
	req.DescriptionSet, req.Compression = false, "ulmo"
	runner.body = []byte("compressed")
	if changed, err := BuildDMGVersion(req); err != nil || changed.Format.Compression != "ulmo" || len(runner.calls) != 2 {
		t.Fatalf("compression change incorrectly reused a version: %#v, %v", changed, err)
	}
	repo, _ := OpenRepo(cfg.ImagesDir, req.Name)
	assertBytes(t, filepath.Join(repo.Dir, dirObjects, first.ID, ImageDMG), []byte("first"))
}

func TestDMGVersionBuildRejectsInvalidOrFailedCreation(t *testing.T) {
	for _, failure := range []string{"name", "shadow", "compression", "description", "tool"} {
		t.Run(failure, func(t *testing.T) {
			cfg, runner := buildConfig(t), &dmgRunner{body: []byte("unpublished")}
			req := DMGRequest{Config: cfg, Name: "alice/app", SrcDir: sourceDir(t), Builder: "test", Run: runner.run}
			switch failure {
			case "name":
				req.Name += ":abcd"
			case "shadow":
				req.Shadow = "shadow"
			case "compression":
				req.Compression = "invalid"
			case "description":
				req.Description = "\nblank first line"
			case "tool":
				runner.err = errors.New("create failed")
			}
			if _, err := BuildDMGVersion(req); err == nil {
				t.Fatal("accepted invalid or failed build")
			}
			if failure != "tool" && len(runner.calls) != 0 {
				t.Fatal("invalid request executed hdiutil")
			}
			if _, err := os.Stat(filepath.Join(cfg.ImagesDir, "alice", "app", fileMeta)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed build exposed metadata: %v", err)
			}
		})
	}
}
