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

	"github.com/jiangxin/tiler/internal/activity"
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
	if err := recoverVersionRemoval(imagesDir, repo.Name(), hooks{}); err != nil {
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
	if _, err := PublishFullVersion(imagesDir, logsFor(imagesDir), repo.Name(), stage, node, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(mustControl(t, imagesDir, repo.Name(), versionRemovalJournal)); !errors.Is(err, os.ErrNotExist) {
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
	if _, err := os.Stat(mustControl(t, imagesDir, repo.Name(), versionRemovalJournal)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal remains: %v", err)
	}
}

func TestRemoveVersionSubtreeLockedCreatesAndCompletesJournal(t *testing.T) {
	imagesDir, repo, nodes := removalFixture(t)
	removed, err := RemoveVersionSubtreeLocked(imagesDir, repo.LogsDir, repo.Name(), nodes[1].ID)
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
	if _, err := os.Stat(mustControl(t, imagesDir, repo.Name(), versionRemovalJournal)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal remains: %v", err)
	}
}

func TestRemoveVersionRepositoryLockedDeletesLeaf(t *testing.T) {
	imagesDir, repo, nodes := removalFixture(t)
	removed, err := RemoveVersionRepositoryLocked(imagesDir, repo.LogsDir, repo.Name())
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 3 || removed[0].ID != nodes[2].ID || removed[2].ID != nodes[0].ID {
		t.Fatalf("removed = %#v", removed)
	}
	if _, err := os.Stat(repo.Dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("repository leaf remains: %v", err)
	}
}

func TestRemoveEmptyVersionRepositoryAndRecoverMissingLeaf(t *testing.T) {
	imagesDir := t.TempDir()
	repo, _ := OpenRepo(imagesDir, "alice/empty")
	if err := os.MkdirAll(repo.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := removalManifest(repo, nil)
	body, err := EncodeVersionManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(repo.MetaPath(), body, 0o644); err != nil {
		t.Fatal(err)
	}
	j := removalJournal{Name: repo.Name(), Before: manifest, After: manifest, Delete: true}
	if err := saveRemovalJournal(imagesDir, j, hooks{}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(repo.Dir); err != nil {
		t.Fatal(err)
	}
	if err := recoverVersionRemoval(imagesDir, repo.Name(), hooks{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(mustControl(t, imagesDir, repo.Name(), versionRemovalJournal)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal remains: %v", err)
	}
}

func removalFixture(t *testing.T) (string, Repo, []ImageMeta) {
	t.Helper()
	imagesDir := t.TempDir()
	repo, _ := OpenRepo(imagesDir, "alice/app")
	repo.LogsDir = logsFor(imagesDir)
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

func writeVersionLog(t *testing.T, repo Repo, id string) string {
	t.Helper()
	dir, name, _, err := repo.activityLogFile(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("log\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".lock", []byte("lock"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRemovingOneVersionLeavesSiblingLogs(t *testing.T) {
	imagesDir, repo, nodes := removalFixture(t)
	paths := make([]string, len(nodes))
	for i, node := range nodes {
		paths[i] = writeVersionLog(t, repo, node.ID)
	}
	if _, err := RemoveVersionSubtreeLocked(imagesDir, repo.LogsDir, repo.Name(), nodes[1].ID); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths[1:] {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("removed log %s remains: %v", path, err)
		}
		if _, err := os.Stat(path + ".lock"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("removed lock %s remains: %v", path, err)
		}
	}
	if _, err := os.Stat(paths[0]); err != nil {
		t.Fatalf("sibling log missing: %v", err)
	}
	if _, err := os.Stat(paths[0] + ".lock"); err != nil {
		t.Fatalf("sibling lock missing: %v", err)
	}
}

func TestRepositoryRemovalDeletesTheImageLogDirectory(t *testing.T) {
	imagesDir, repo, nodes := removalFixture(t)
	for _, node := range nodes {
		writeVersionLog(t, repo, node.ID)
	}
	neighbor := filepath.Join(repo.LogsDir, "images", "alice", "neighbor", "keep.log")
	if err := os.MkdirAll(filepath.Dir(neighbor), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(neighbor, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RemoveVersionRepositoryLocked(imagesDir, repo.LogsDir, repo.Name()); err != nil {
		t.Fatal(err)
	}
	app := filepath.Join(repo.LogsDir, "images", "alice", "app")
	if _, err := os.Stat(app); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("image log directory remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo.LogsDir, "images")); err != nil {
		t.Fatalf("logs/images was removed: %v", err)
	}
	if data, err := os.ReadFile(neighbor); err != nil || string(data) != "keep" {
		t.Fatalf("neighbor log = %q, %v", data, err)
	}
}

func TestLastVersionLogRemovesEmptyParentSegments(t *testing.T) {
	imagesDir, repo, nodes := removalFixture(t)
	for _, node := range nodes {
		writeVersionLog(t, repo, node.ID)
	}
	if _, err := RemoveVersionSubtreeLocked(imagesDir, repo.LogsDir, repo.Name(), nodes[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo.LogsDir, "images", "alice")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty image parent remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo.LogsDir, "images")); err != nil {
		t.Fatalf("logs/images was removed: %v", err)
	}
}

func TestReplayUsesTheJournalLogsDir(t *testing.T) {
	imagesDir, repo, nodes := removalFixture(t)
	recorded := t.TempDir()
	current := repo.LogsDir
	repo.LogsDir = recorded
	recordedLog := writeVersionLog(t, repo, nodes[2].ID)
	repo.LogsDir = current
	currentLog := writeVersionLog(t, repo, nodes[2].ID)
	j := removalJournal{Name: repo.Name(), Before: removalManifest(repo, nodes),
		After: removalManifest(repo, nodes[:2]), IDs: []string{nodes[2].ID}, LogsDir: recorded}
	if err := saveRemovalJournal(imagesDir, j, hooks{}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(mustControl(t, imagesDir, repo.Name(), versionRemovalJournal))
	if err != nil || !strings.Contains(string(body), recorded) {
		t.Fatalf("journal = %s, %v", body, err)
	}
	if err := replayVersionRemoval(imagesDir, j, hooks{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(recordedLog); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal log remains: %v", err)
	}
	if _, err := os.Stat(recordedLog + ".lock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal lock remains: %v", err)
	}
	if _, err := os.Stat(currentLog); err != nil {
		t.Fatalf("current config log was removed: %v", err)
	}
}

func TestOldJournalLeavesTheLogsTreeAlone(t *testing.T) {
	imagesDir, repo, nodes := removalFixture(t)
	logPath := writeVersionLog(t, repo, nodes[2].ID)
	j := removalJournal{Name: repo.Name(), Before: removalManifest(repo, nodes),
		After: removalManifest(repo, nodes[:2]), IDs: []string{nodes[2].ID}}
	if err := saveRemovalJournal(imagesDir, j, hooks{}); err != nil {
		t.Fatal(err)
	}
	if err := replayVersionRemoval(imagesDir, j, hooks{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("old journal removed the new log: %v", err)
	}
	object, err := repo.ObjectDirPath(nodes[2].ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(object); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("object directory remains: %v", err)
	}
}

func TestReplayRefusesASymlinkVersionLog(t *testing.T) {
	imagesDir, repo, nodes := removalFixture(t)
	dir, name, _, err := repo.activityLogFile(nodes[2].ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(external, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, name)
	if err := os.Symlink(external, link); err != nil {
		t.Skip(err)
	}
	j := removalJournal{Name: repo.Name(), Before: removalManifest(repo, nodes),
		After: removalManifest(repo, nodes[:2]), IDs: []string{nodes[2].ID}, LogsDir: repo.LogsDir}
	if err := saveRemovalJournal(imagesDir, j, hooks{}); err != nil {
		t.Fatal(err)
	}
	err = replayVersionRemoval(imagesDir, j, hooks{})
	if !errors.Is(err, ErrCleanupPending) || !errors.Is(err, activity.ErrUnsafeFile) {
		t.Fatalf("replay = %v", err)
	}
	if data, readErr := os.ReadFile(external); readErr != nil || string(data) != "keep" {
		t.Fatalf("symlink was followed: %q, %v", data, readErr)
	}
	info, statErr := os.Lstat(link)
	if statErr != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink log was removed: %v", statErr)
	}
}

func TestVersionSetRemovalDeletesOnlyTheApprovedLog(t *testing.T) {
	_, repo, nodes := removalFixture(t)
	paths := make([]string, len(nodes))
	for i, node := range nodes {
		paths[i] = writeVersionLog(t, repo, node.ID)
	}
	if err := RemoveVersionSetLocked(repo, []string{nodes[2].ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(paths[2]); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("approved log remains: %v", err)
	}
	if _, err := os.Stat(paths[2] + ".lock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("approved lock remains: %v", err)
	}
	for _, path := range paths[:2] {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("sibling log missing: %v", err)
		}
	}
}

func TestRecoveryDeletesLogsAfterTheLeafIsGone(t *testing.T) {
	imagesDir, repo, nodes := removalFixture(t)
	for _, node := range nodes {
		writeVersionLog(t, repo, node.ID)
	}
	ids := []string{nodes[0].ID, nodes[1].ID, nodes[2].ID}
	j := removalJournal{Name: repo.Name(), Before: removalManifest(repo, nodes),
		After: removalManifest(repo, nil), IDs: ids, Delete: true, LogsDir: repo.LogsDir}
	if err := saveRemovalJournal(imagesDir, j, hooks{}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(repo.Dir); err != nil {
		t.Fatal(err)
	}
	if err := recoverVersionRemoval(imagesDir, repo.Name(), hooks{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo.LogsDir, "images", "alice")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovered image log directory remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo.LogsDir, "images")); err != nil {
		t.Fatalf("logs/images was removed: %v", err)
	}
}
