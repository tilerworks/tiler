package image

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCheckImportCollisionsPlansEveryBranch(t *testing.T) {
	repo, node, src := linkedNode(t, strings.Repeat("ab", 32), []byte("vendor-payload"), 50)
	manifest := versionManifest(repo, node)
	ref := payloadText(t, repo, node)
	alias := filepath.Join(filepath.Dir(src), "alias.sqsh")
	if err := os.Symlink(src, alias); err != nil {
		t.Fatal(err)
	}
	sourceStamp := importStamp(t, filepath.Dir(src))
	other, otherSize := collisionSquash(t, []byte("other-payload"), 50)
	otherStamp := importStamp(t, filepath.Dir(other))
	otherNode := fullNode(strings.Repeat("b", 64), otherSize, 50)

	kept := func(t *testing.T, mode ImportMode, action MergeAction, cand ImportCandidate) {
		t.Helper()
		got := mustMerge(t, repo, mode, manifest, []ImportCandidate{cand})
		requireKept(t, got.Items[0], action, node)
	}
	kept(t, ImportLink, MergeNoop, ImportCandidate{Node: incoming(node), Payloads: oneSource(alias, false)})
	kept(t, ImportCopy, MergePromote, ImportCandidate{Node: incoming(node), Payloads: oneSource(src, true)})
	if payloadText(t, repo, node) != ref {
		t.Fatal("promote rewrote the location file")
	}
	mustConflict(t, repo, ImportLink, manifest, cand(incoming(node), other), "source conflict")
	mustConflict(t, repo, ImportCopy, manifest, cand(incoming(node), other, true), "source conflict")
	mustConflict(t, repo, ImportCopy, manifest, cand(incoming(node), src, false), "content conflict")
	changed := incoming(node)
	changed.MtimeUnix++
	mustConflict(t, repo, ImportLink, manifest, cand(changed, src), "source attribute conflict")

	localRepo, local, localSrc := localNode(t, strings.Repeat("c", 64), []byte("local-payload"), 50)
	localManifest := versionManifest(localRepo, local)
	localCand := incoming(local)
	localCand.MtimeUnix += 5
	keptLocal := mustMerge(t, localRepo, ImportCopy, localManifest, []ImportCandidate{{
		Node: localCand, Payloads: oneSource(localSrc, true),
	}})
	requireKept(t, keptLocal.Items[0], MergeNoop, local)
	requireKept(t, mustMerge(t, localRepo, ImportLink, localManifest, []ImportCandidate{{
		Node: incoming(local), Payloads: oneSource(localSrc, false),
	}}).Items[0], MergeRetain, local)

	moved := incoming(node)
	moved.Parent = strings.Repeat("d", 64)
	mustConflict(t, repo, ImportLink, manifest, cand(moved, src), "identity conflict")
	if importStamp(t, filepath.Dir(src)) != sourceStamp {
		t.Fatal("external source changed")
	}
	if err := os.Remove(src); err != nil {
		t.Fatal(err)
	}
	mustConflict(t, repo, ImportCopy, manifest, cand(incoming(node), src, true), "payload conflict")
	if payloadText(t, repo, node) != ref {
		t.Fatal("conflict rewrote the location file")
	}
	fresh := collisionRepo(t)
	added := mustMerge(t, fresh, ImportLink, versionManifest(fresh), []ImportCandidate{{
		Node: otherNode, Payloads: oneSource(other, false),
	}})
	if len(added.Items) != 1 || added.Items[0].Action != MergeAdd || added.Items[0].Node.ID != otherNode.ID ||
		added.Items[0].Node.Description != otherNode.Description {
		t.Fatalf("add = %+v", added.Items)
	}
	copied := mustMerge(t, fresh, ImportCopy, versionManifest(fresh), []ImportCandidate{{
		Node: otherNode, Payloads: oneSource(other, true),
	}})
	if copied.Items[0].Action != MergeAdd {
		t.Fatalf("copy add = %+v", copied.Items[0])
	}
	if importStamp(t, filepath.Dir(other)) != otherStamp {
		t.Fatal("external source changed")
	}
	if _, err := os.Stat(filepath.Join(repo.Dir, fileMeta)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("checker published metadata")
	}
}

func TestCheckImportCollisionsRejectsGraphAndUnknownSlots(t *testing.T) {
	repo, zero, src := linkedNode(t, UnknownSHA256, []byte("unknown-payload"), 40)
	manifest := versionManifest(repo, zero)
	requireKept(t, mustMerge(t, repo, ImportLink, manifest, []ImportCandidate{{
		Node: incoming(zero), Payloads: oneSource(src, false),
	}}).Items[0], MergeNoop, zero)
	other, _ := collisionSquash(t, []byte("second-source"), 40)
	mustConflict(t, repo, ImportLink, manifest, cand(incoming(zero), other), "source conflict")
	facts := incoming(zero)
	facts.MtimeUnix++
	mustConflict(t, repo, ImportLink, manifest, cand(facts, src), "source attribute conflict")
	known := fullNode(strings.Repeat("e", 64), zero.Size, zero.MtimeUnix)
	got := mustMerge(t, repo, ImportCopy, manifest, []ImportCandidate{{
		Node: known, Payloads: oneSource(src, true),
	}})
	if len(got.Items) != 1 || got.Items[0].Action != MergeAdd || got.Items[0].Node.ID != known.ID || len(manifest.Images) != 1 {
		t.Fatalf("known id = %+v manifest %d", got.Items, len(manifest.Images))
	}
	drift := known
	drift.MtimeUnix++
	mustConflict(t, repo, ImportCopy, manifest, cand(drift, src, true), "unknown source conflict")
	mustConflict(t, repo, ImportCopy, manifest, cand(zero, src, true), "reserved unknown digest")

	parent := fullNode(strings.Repeat("a", 64), zero.Size, zero.MtimeUnix)
	child := parent
	child.ID, child.SHA256 = strings.Repeat("f", 64), strings.Repeat("f", 64)
	child.Kind, child.Parent = KindIncremental, parent.ID
	both := mustMerge(t, collisionRepo(t), ImportLink, versionManifest(repo), []ImportCandidate{
		{Node: parent, Payloads: oneSource(src, false)},
		{Node: child, Payloads: oneSource(src, false)},
	})
	if len(both.Items) != 2 || both.Items[0].Action != MergeAdd || both.Items[1].Action != MergeAdd {
		t.Fatalf("graph = %+v", both.Items)
	}
	orphan := child
	orphan.Parent = strings.Repeat("9", 64)
	mustConflict(t, collisionRepo(t), ImportLink, versionManifest(repo), cand(orphan, src), "parent")
	bad := incoming(zero)
	bad.Parent = parent.ID
	mustConflict(t, repo, ImportLink, manifest, []ImportCandidate{
		{Node: known, Payloads: oneSource(other, true)},
		{Node: bad, Payloads: oneSource(src, false)},
	}, "identity conflict")
	if _, err := CheckImportCollisions(repo, "both", manifest, nil); err == nil || !strings.Contains(err.Error(), "link or copy") {
		t.Fatal(err)
	}
}

func TestCheckImportCollisionsComparesBlockSources(t *testing.T) {
	repo := collisionRepo(t)
	left, leftSize := collisionSquash(t, []byte("block-zero"), 70)
	right, rightSize := collisionSquash(t, []byte("block-one"), 70)
	elsewhere, _ := collisionSquash(t, []byte("block-else"), 70)
	id := strings.Repeat("1", 64)
	node := fullNode(id, leftSize+rightSize, 70)
	node.Blocks = []BlockEntry{
		{Name: "blk.000", Size: leftSize, SHA256: strings.Repeat("1", 64), MtimeUnix: 70},
		{Name: "blk.001", Size: rightSize, SHA256: strings.Repeat("2", 64), MtimeUnix: 70},
	}
	seedBlockLink(t, repo, id, node.Blocks[0], left)
	seedBlockLink(t, repo, id, node.Blocks[1], right)
	manifest := versionManifest(repo, node)
	payloads := []ImportPayload{{Block: "blk.000", Source: left}, {Block: "blk.001", Source: right}}
	requireKept(t, mustMerge(t, repo, ImportLink, manifest, []ImportCandidate{{
		Node: incoming(node), Payloads: payloads,
	}}).Items[0], MergeNoop, node)
	moved := append([]ImportPayload{}, payloads...)
	moved[1].Source = elsewhere
	mustConflict(t, repo, ImportCopy, manifest, []ImportCandidate{{
		Node: incoming(node), Payloads: []ImportPayload{
			{Block: "blk.000", Source: left, ContentOK: true},
			{Block: "blk.001", Source: elsewhere, ContentOK: true},
		},
	}}, "source conflict")
}

func linkedNode(t *testing.T, id string, payload []byte, mtime int64) (Repo, ImageMeta, string) {
	t.Helper()
	repo := collisionRepo(t)
	src, size := collisionSquash(t, payload, mtime)
	node := fullNode(id, size, mtime)
	seedLink(t, repo, node, src)
	return repo, node, src
}

func localNode(t *testing.T, id string, payload []byte, mtime int64) (Repo, ImageMeta, string) {
	t.Helper()
	repo, node, src := linkedNode(t, id, payload, mtime)
	path, err := repo.ObjectPayloadPath(node)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, mustRead(t, src), 0o644); err != nil {
		t.Fatal(err)
	}
	when := time.Unix(mtime, 0)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
	return repo, node, src
}

func collisionRepo(t *testing.T) Repo {
	t.Helper()
	repo, err := OpenRepo(t.TempDir(), "vendor/app")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(repo.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return repo
}

func collisionSquash(t *testing.T, payload []byte, mtime int64) (string, int64) {
	t.Helper()
	path := writeFakeSquashfs(t, t.TempDir(), payload)
	when := time.Unix(mtime, 0)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.ModTime().Unix() != mtime {
		t.Fatalf("mtime %v err %v", info, err)
	}
	return path, info.Size()
}

func fullNode(id string, size, mtime int64) ImageMeta {
	return ImageMeta{
		ID: id, SHA256: id, Kind: KindFull, Size: size, MtimeUnix: mtime,
		Format:    Format{Type: FormatSquashfs, Compression: CompressionZstd},
		CreatedAt: time.Unix(10, 0).UTC(), Builder: "target", Description: "keep",
	}
}

func incoming(node ImageMeta) ImageMeta {
	node.Description = "incoming"
	node.Builder = "import"
	node.CreatedAt = time.Unix(99, 0).UTC()
	return node
}

func oneSource(path string, verified bool) []ImportPayload {
	return []ImportPayload{{Source: path, ContentOK: verified}}
}

func cand(node ImageMeta, src string, verified ...bool) []ImportCandidate {
	ok := len(verified) == 1 && verified[0]
	return []ImportCandidate{{Node: node, Payloads: oneSource(src, ok)}}
}

func versionManifest(repo Repo, nodes ...ImageMeta) VersionManifest {
	return VersionManifest{Version: VersionManifestVersion, Name: repo.Name(), Images: nodes}
}

func seedLink(t *testing.T, repo Repo, node ImageMeta, src string) {
	t.Helper()
	path, err := repo.ObjectPayloadPath(node)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writePathRef(src, path); err != nil {
		t.Fatal(err)
	}
}

func seedBlockLink(t *testing.T, repo Repo, id string, block BlockEntry, src string) {
	t.Helper()
	path, err := repo.ObjectBlockPath(id, block.Name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writePathRef(src, path); err != nil {
		t.Fatal(err)
	}
}

func payloadText(t *testing.T, repo Repo, node ImageMeta) string {
	t.Helper()
	path, err := repo.ObjectPayloadPath(node)
	if err != nil {
		t.Fatal(err)
	}
	return string(mustRead(t, path))
}

func mustMerge(t *testing.T, repo Repo, mode ImportMode, manifest VersionManifest, cands []ImportCandidate) ImportMerge {
	t.Helper()
	got, err := CheckImportCollisions(repo, mode, manifest, cands)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func mustConflict(t *testing.T, repo Repo, mode ImportMode, manifest VersionManifest, cands []ImportCandidate, want string) {
	t.Helper()
	got, err := CheckImportCollisions(repo, mode, manifest, cands)
	if err == nil || !strings.Contains(err.Error(), want) || len(got.Items) != 0 {
		t.Fatalf("got %+v err %v, want %q", got.Items, err, want)
	}
}

func requireKept(t *testing.T, item ImportMergeItem, action MergeAction, old ImageMeta) {
	t.Helper()
	if item.Action != action || item.Node.Description != old.Description || item.Node.Builder != old.Builder ||
		!item.Node.CreatedAt.Equal(old.CreatedAt) || item.Node.MtimeUnix != old.MtimeUnix {
		t.Fatalf("%s = %+v, want %s kept", action, item, old.Description)
	}
}
