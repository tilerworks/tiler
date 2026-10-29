package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/image"
)

type statusVolume struct {
	image  string
	shadow string
	point  string
}

func restoreDiskSeams(t *testing.T) {
	t.Helper()
	origTable, origInfo, origRun := loadMountTable, readHdiutilInfo, runHdiutil
	t.Cleanup(func() {
		loadMountTable, readHdiutilInfo, runHdiutil = origTable, origInfo, origRun
	})
}

func hdiutilStatusPlist(volumes []statusVolume) []byte {
	var body strings.Builder
	body.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	body.WriteString(`<plist version="1.0"><dict><key>images</key><array>`)
	for _, volume := range volumes {
		body.WriteString(`<dict><key>image-path</key><string>` + volume.image + `</string>`)
		if volume.shadow != "" {
			body.WriteString(`<key>shadow-path</key><string>` + volume.shadow + `</string>`)
		}
		body.WriteString(`<key>system-entities</key><array><dict><key>mount-point</key><string>` + volume.point + `</string></dict></array></dict>`)
	}
	body.WriteString(`</array></dict></plist>`)
	return []byte(body.String())
}

var splitMtime = time.Unix(1772600767, 0).UTC()

const concatViewFile = image.ImageFile + "-concat-view"

func sumOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func writeSplitFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, splitMtime, splitMtime); err != nil {
		t.Fatal(err)
	}
}

func storeFixtureMeta(t *testing.T, repoDir string, manifest image.Manifest) {
	t.Helper()
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	images := append([]image.ImageMeta{}, manifest.Images...)
	for i := range images {
		if images[i].Format == (image.Format{}) {
			images[i].Format = manifest.Format
		}
	}
	body, err := image.EncodeVersionManifest(image.VersionManifest{
		Version: image.VersionManifestVersion, Name: manifest.Name, Images: images,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "meta.yaml"), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeBlocksFixture(t *testing.T, imagesDir, name string, bodies []string, view bool) string {
	t.Helper()
	repo, err := image.OpenRepo(imagesDir, name)
	if err != nil {
		t.Fatal(err)
	}
	var whole []byte
	for _, body := range bodies {
		whole = append(whole, body...)
	}
	sum := sha256.Sum256(whole)
	id := image.ID(sum)
	node := image.ImageMeta{ID: id, Kind: image.KindFull, Size: int64(len(whole)), SHA256: id,
		MtimeUnix: splitMtime.Unix(), CreatedAt: splitMtime, Builder: "test"}
	dir := filepath.Join(repo.Dir, "_blocks", id)
	var names []string
	for i, body := range bodies {
		name := fmt.Sprintf("blk.%03d", i)
		writeSplitFile(t, filepath.Join(dir, name), body)
		names = append(names, name)
		node.Blocks = append(node.Blocks, image.BlockEntry{Name: name, Size: int64(len(body)), SHA256: sumOf([]byte(body)), MtimeUnix: splitMtime.Unix()})
	}
	if view {
		writeSplitFile(t, filepath.Join(dir, concatViewFile), strings.Join(names, "\n")+"\n")
	}
	storeFixtureMeta(t, repo.Dir, image.Manifest{Version: image.VersionManifestVersion, Name: repo.Name(),
		Format: image.Format{Type: image.FormatSquashfs, Compression: image.CompressionZstd}, Head: id, Images: []image.ImageMeta{node}})
	return id
}
