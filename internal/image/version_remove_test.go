package image

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/config"
)

func TestVersionRemovalCommitsBeforePostorderCleanupAndRecovers(t *testing.T) {
	imagesDir, repo, nodes := removalFixture(t)
	journal := removalJournal{
		Name: repo.Name(), Before: removalManifest(repo, nodes),
		After: removalManifest(repo, nodes[:1]), IDs: []string{nodes[2].ID, nodes[1].ID},
	}
	if err := saveRemovalJournal(imagesDir, journal, hooks{}); err != nil {
		t.Fatal(err)
	}
	var cleaned []string
	stop := errors.New("interrupted cleanup")
	err := replayVersionRemoval(imagesDir, journal, hooks{before: func(old, destination string) error {
		if destination == "" {
			cleaned = append(cleaned, filepath.Base(old))
			if len(cleaned) == 2 {
				return stop
			}
		}
		return nil
	}})
	if !errors.Is(err, ErrCleanupPending) || strings.Join(cleaned, ",") != nodes[2].ID+","+nodes[1].ID {
		t.Fatalf("cleanup = %v, %v", cleaned, err)
	}
	manifest, loadErr := LoadVersionManifest(repo)
	if loadErr != nil || len(manifest.Images) != 1 {
		t.Fatalf("committed manifest = %#v, %v", manifest, loadErr)
	}
	if err := recoverVersionRemoval(imagesDir, hooks{}); err != nil {
		t.Fatal(err)
	}
	for _, node := range nodes[1:] {
		dir, _ := repo.ObjectDirPath(node.ID)
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("object %s remains: %v", node.ID, err)
		}
	}
}

func TestPublishRecoversRemovalBeforeReusingID(t *testing.T) {
	imagesDir, repo, nodes := removalFixture(t)
	j := removalJournal{Name: repo.Name(), Before: removalManifest(repo, nodes),
		After: removalManifest(repo, nodes[:1]), IDs: []string{nodes[1].ID, nodes[2].ID}}
	if err := saveRemovalJournal(imagesDir, j, hooks{}); err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(t.TempDir(), "payload")
	body := []byte("replacement")
	if err := os.WriteFile(stage, body, 0o600); err != nil {
		t.Fatal(err)
	}
	node := removalTestNode(body, "", KindFull, 4)
	if _, err := PublishFullVersion(imagesDir, repo.Name(), stage, node, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(imagesDir, versionRemovalJournal)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal remains: %v", err)
	}
}

func TestDMGCommitRecoversRemovalBeforeReadingMetadata(t *testing.T) {
	imagesDir, repo, nodes := removalFixture(t)
	j := removalJournal{Name: repo.Name(), Before: removalManifest(repo, nodes),
		After: removalManifest(repo, nodes[:1]), IDs: []string{nodes[2].ID, nodes[1].ID}}
	if err := saveRemovalJournal(imagesDir, j, hooks{}); err != nil {
		t.Fatal(err)
	}
	req := DMGRequest{Config: config.Config{ImagesDir: imagesDir}, Name: repo.Name(), Compression: "zlib"}
	if _, err := commitDMGVersion(repo, req, "", "new-content"); err != nil {
		t.Fatal(err)
	}
	manifest, err := LoadVersionManifest(repo)
	if err != nil || len(manifest.Images) != 1 {
		t.Fatalf("recovered manifest = %#v, %v", manifest, err)
	}
	if _, err := os.Stat(filepath.Join(imagesDir, versionRemovalJournal)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal remains: %v", err)
	}
}

func TestRemoveVersionSubtreeLockedCreatesAndCompletesJournal(t *testing.T) {
	imagesDir, repo, nodes := removalFixture(t)
	removed, err := RemoveVersionSubtreeLocked(imagesDir, repo.Name(), nodes[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 2 || removed[0].ID != nodes[2].ID || removed[1].ID != nodes[1].ID {
		t.Fatalf("removed = %#v", removed)
	}
	manifest, err := LoadVersionManifest(repo)
	if err != nil || len(manifest.Images) != 1 || manifest.Images[0].ID != nodes[0].ID {
		t.Fatalf("remaining manifest = %#v, %v", manifest, err)
	}
	if _, err := os.Stat(filepath.Join(imagesDir, versionRemovalJournal)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal remains: %v", err)
	}
}

func removalFixture(t *testing.T) (string, Repo, []ImageMeta) {
	t.Helper()
	imagesDir := t.TempDir()
	repo, _ := OpenRepo(imagesDir, "alice/app")
	bodies := [][]byte{[]byte("root"), []byte("child"), []byte("tip")}
	nodes := []ImageMeta{removalTestNode(bodies[0], "", KindFull, 1)}
	nodes = append(nodes, removalTestNode(bodies[1], nodes[0].ID, KindIncremental, 2))
	nodes = append(nodes, removalTestNode(bodies[2], nodes[1].ID, KindIncremental, 3))
	if err := os.MkdirAll(repo.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i, node := range nodes {
		path, _ := repo.ObjectPayloadPath(node)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, bodies[i], 0o644); err != nil {
			t.Fatal(err)
		}
	}
	body, err := EncodeVersionManifest(removalManifest(repo, nodes))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(repo.MetaPath(), body, 0o644); err != nil {
		t.Fatal(err)
	}
	return imagesDir, repo, nodes
}

func removalManifest(repo Repo, nodes []ImageMeta) VersionManifest {
	return VersionManifest{Version: VersionManifestVersion, Name: repo.Name(), Images: nodes}
}

func removalTestNode(body []byte, parent, kind string, minute int) ImageMeta {
	id := fmtDigest(body)
	return ImageMeta{ID: id, SHA256: id, Size: int64(len(body)), Parent: parent,
		Kind: kind, Builder: "test", Format: Format{Type: FormatSquashfs, Compression: "gzip"},
		CreatedAt: time.Date(2026, 1, 1, 0, minute, 0, 0, time.UTC)}
}

func fmtDigest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
