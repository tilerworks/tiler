package image

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// splitFixture pairs a valid split meta with the payload file bodies
// its manifest was derived from, in manifest order.
type splitFixture struct {
	meta   Meta
	bodies [][]byte
}

// manifest is the v2 DAG form of the fixture: the linear chain
// becomes the node list and the head is the chain tail.
func (fx splitFixture) manifest() Manifest {
	return manifestFromMeta(fx.meta)
}

func TestPublishStagedBlocksPayloadFresh(t *testing.T) {
	repo := testRepo(t)
	fx := blocksFixture([][]byte{[]byte("block-a"), []byte("block-b")})
	if err := publishSplit(t, repo, fx, nil, hooks{}); err != nil {
		t.Fatal(err)
	}
	assertActivePayload(t, repo, fx)
	assertNoTemps(t, repo.Dir)
}

func TestPublishStagedPayloadArchivesUnreferencedPrevious(t *testing.T) {
	repo := testRepo(t)
	old := blocksFixture([][]byte{[]byte("old-a"), []byte("old-b")})
	seedSplitActive(t, repo, old)
	next := blocksFixture([][]byte{[]byte("new-a"), []byte("new-b"), []byte("new-c")})
	if err := publishSplit(t, repo, next, nil, hooks{}); err != nil {
		t.Fatal(err)
	}
	assertActivePayload(t, repo, next)
	arch, err := repo.ArchivePath(old.meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, filepath.Join(holdPayloadPath(arch, old.meta), "blk.000"), []byte("old-a"))
	assertBytes(t, filepath.Join(holdPayloadPath(arch, old.meta), "blk.001"), []byte("old-b"))
	loaded, err := Load(filepath.Join(arch, fileMeta))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != old.meta.ID || len(loaded.Blocks) != 2 {
		t.Fatalf("archived meta = %+v", loaded)
	}
	assertGone(t, filepath.Join(repo.Dir, dirHold, old.meta.ID))
	assertNoTemps(t, repo.Dir)
}

func TestPublishStagedPayloadHoldsReferencedPrevious(t *testing.T) {
	repo := testRepo(t)
	old := blocksFixture([][]byte{[]byte("held-a"), []byte("held-b")})
	seedSplitActive(t, repo, old)
	next := blocksFixture([][]byte{[]byte("fresh-a"), []byte("fresh-b")})
	refs := holdSet{ids: map[string]bool{old.meta.ID: true}}
	if err := publishSplit(t, repo, next, refs, hooks{}); err != nil {
		t.Fatal(err)
	}
	assertActivePayload(t, repo, next)
	hold, err := repo.HoldPath(old.meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, filepath.Join(holdPayloadPath(hold, old.meta), "blk.000"),
		[]byte("held-a"))
	if _, err := Load(filepath.Join(hold, fileMeta)); err != nil {
		t.Fatal(err)
	}
	assertGone(t, filepath.Join(repo.Dir, dirArchives, old.meta.ID))
	assertNoTemps(t, repo.Dir)
}

func TestPublishStagedPayloadReplacesSingleForm(t *testing.T) {
	repo := testRepo(t)
	old := seedActive(t, repo, []byte("previous-full"), false)
	next := blocksFixture([][]byte{[]byte("split-a")})
	if err := publishSplit(t, repo, next, nil, hooks{}); err != nil {
		t.Fatal(err)
	}
	assertActivePayload(t, repo, next)
	arch, err := repo.ArchivePath(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, filepath.Join(arch, fileImage), []byte("previous-full"))
	assertNoTemps(t, repo.Dir)
}

func TestPublishStagedPayloadNoOp(t *testing.T) {
	repo := testRepo(t)
	fx := blocksFixture([][]byte{[]byte("same-a")})
	if err := publishSplit(t, repo, fx, nil, hooks{}); err != nil {
		t.Fatal(err)
	}
	if err := publishSplit(t, repo, fx, nil, hooks{}); err != nil {
		t.Fatal(err)
	}
	assertActivePayload(t, repo, fx)
	assertNoTemps(t, repo.Dir)
}

func TestPublishStagedPayloadRejectsMismatch(t *testing.T) {
	repo := testRepo(t)
	fx := blocksFixture([][]byte{[]byte("block-a"), []byte("block-b")})
	corrupt := splitFixture{meta: fx.meta,
		bodies: [][]byte{[]byte("block-a"), []byte("torn-write")}}
	payloadTmp, metaTmp := stageSplitPayload(t, repo, corrupt)
	err := publishStagedPayload(repo, fx.manifest(), nil, payloadTmp,
		finalPayloadDir(t, repo, fx.meta), metaTmp, hooks{})
	if err == nil || !strings.Contains(err.Error(), "payload does not match meta") {
		t.Fatalf("err = %v", err)
	}
	assertGone(t, filepath.Join(repo.Dir, dirBlocks))
	assertGone(t, repo.MetaPath())
	assertNoTemps(t, repo.Dir)
}

func TestPublishStagedPayloadRejectsSingleMeta(t *testing.T) {
	repo := testRepo(t)
	meta := validMeta()
	if err := os.MkdirAll(repo.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	name, err := tempBlocksDirName()
	if err != nil {
		t.Fatal(err)
	}
	payloadTmp := filepath.Join(repo.Dir, name)
	if err := os.Mkdir(payloadTmp, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := manifestFromMeta(meta)
	metaTmp := stageManifest(t, repo, manifest)
	err = publishStagedPayload(repo, manifest, nil, payloadTmp, repo.ImagePath(), metaTmp, hooks{})
	if err == nil || !strings.Contains(err.Error(), "requires a split manifest") {
		t.Fatalf("err = %v", err)
	}
	assertNoTemps(t, repo.Dir)
}

func TestPublishStagedPayloadMetaRenameFailureRestoresPrevious(t *testing.T) {
	repo := testRepo(t)
	old := blocksFixture([][]byte{[]byte("old-a")})
	seedSplitActive(t, repo, old)
	next := blocksFixture([][]byte{[]byte("new-a")})
	seam := hooks{before: func(_, newpath string) error {
		if newpath == repo.MetaPath() {
			return errors.New("meta rename refused")
		}
		return nil
	}}
	if err := publishSplit(t, repo, next, nil, seam); err == nil {
		t.Fatal("expected meta rename failure")
	}
	assertActivePayload(t, repo, old)
	assertGone(t, filepath.Join(repo.Dir, dirBlocks, next.meta.ID))
	assertGone(t, filepath.Join(repo.Dir, dirHold))
	assertNoTemps(t, repo.Dir)
}

func TestPublishStagedPayloadRenameFailureRestoresPrevious(t *testing.T) {
	repo := testRepo(t)
	old := blocksFixture([][]byte{[]byte("old-a")})
	seedSplitActive(t, repo, old)
	next := blocksFixture([][]byte{[]byte("new-a")})
	final := finalPayloadDir(t, repo, next.meta)
	seam := hooks{before: func(_, newpath string) error {
		if newpath == final {
			return errors.New("payload rename refused")
		}
		return nil
	}}
	payloadTmp, metaTmp := stageSplitPayload(t, repo, next)
	err := publishStagedPayload(repo, next.manifest(), nil, payloadTmp, final, metaTmp, seam)
	if err == nil {
		t.Fatal("expected payload rename failure")
	}
	assertActivePayload(t, repo, old)
	assertGone(t, filepath.Join(repo.Dir, dirHold))
	assertNoTemps(t, repo.Dir)
}

func TestHoldRestoreSymmetryForSplitPayloads(t *testing.T) {
	fx := blocksFixture([][]byte{[]byte("sym-a"), []byte("sym-b")})
	repo := testRepo(t)
	seedSplitActive(t, repo, fx)
	if err := moveActiveToHold(repo, fx.manifest(), hooks{}); err != nil {
		t.Fatal(err)
	}
	assertGone(t, finalPayloadDir(t, repo, fx.meta))
	assertGone(t, repo.MetaPath())
	hold, err := repo.HoldPath(fx.meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertPayloadAt(t, holdPayloadPath(hold, fx.meta), fx)
	if _, err := Load(filepath.Join(hold, fileMeta)); err != nil {
		t.Fatal(err)
	}
	if err := restoreHold(repo, fx.meta.ID, hooks{}); err != nil {
		t.Fatal(err)
	}
	assertActivePayload(t, repo, fx)
	assertGone(t, hold)
}

func TestPublishHoldRestoreChainBlockDirs(t *testing.T) {
	repo := testRepo(t)
	fx, layer := seedBlocksWithChain(t, repo, []byte("layer-body"))
	if err := moveActiveToHold(repo, fx.manifest(), hooks{}); err != nil {
		t.Fatal(err)
	}
	assertGone(t, filepath.Join(repo.Dir, dirBlocks))
	hold, err := repo.HoldPath(fx.meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertPayloadAt(t, holdPayloadPath(hold, fx.meta), fx)
	assertBytes(t, filepath.Join(hold, dirBlocks, layer, "blk.000"), []byte("layer-body"))
	if err := restoreHold(repo, fx.meta.ID, hooks{}); err != nil {
		t.Fatal(err)
	}
	assertActivePayload(t, repo, fx)
	layerDir, err := repo.BlocksDirPath(layer)
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, filepath.Join(layerDir, "blk.000"), []byte("layer-body"))
	assertGone(t, hold)
	assertGone(t, filepath.Join(repo.Dir, dirHold))
}

func TestRestoreHoldMergesChainBlockDir(t *testing.T) {
	repo := testRepo(t)
	fx, layer := seedBlocksWithChain(t, repo, []byte("layer-body"))
	if err := moveActiveToHold(repo, fx.manifest(), hooks{}); err != nil {
		t.Fatal(err)
	}
	hold, err := repo.HoldPath(fx.meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo.Dir, dirBlocks), 0o755); err != nil {
		t.Fatal(err)
	}
	fullDst, err := repo.BlocksDirPath(fx.meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(hold, dirBlocks, fx.meta.ID), fullDst); err != nil {
		t.Fatal(err)
	}
	if err := restoreHold(repo, fx.meta.ID, hooks{}); err != nil {
		t.Fatal(err)
	}
	layerDir, err := repo.BlocksDirPath(layer)
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, filepath.Join(layerDir, "blk.000"), []byte("layer-body"))
	assertActivePayload(t, repo, fx)
	assertGone(t, filepath.Join(hold, dirBlocks, layer))
	assertGone(t, hold)
}

func TestPublishArchivesChainBlockDirs(t *testing.T) {
	repo := testRepo(t)
	fx, layer := seedBlocksWithChain(t, repo, []byte("layer-body"))
	next := []byte("replacement-full")
	if err := repo.Publish(next, manifestFor(next), nil); err != nil {
		t.Fatal(err)
	}
	assertBytes(t, repo.ImagePath(), next)
	assertGone(t, filepath.Join(repo.Dir, dirBlocks, fx.meta.ID))
	assertGone(t, filepath.Join(repo.Dir, dirBlocks, layer))
	arch, err := repo.ArchivePath(fx.meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertPayloadAt(t, holdPayloadPath(arch, fx.meta), fx)
	assertBytes(t, filepath.Join(arch, dirBlocks, layer, "blk.000"), []byte("layer-body"))
	assertGone(t, filepath.Join(repo.Dir, dirHold, fx.meta.ID))
}

func TestPublishRepublishReturnsChainBlockDirs(t *testing.T) {
	repo := testRepo(t)
	fx, layer := seedBlocksWithChain(t, repo, []byte("layer-body"))
	other := blocksFixture([][]byte{[]byte("other-full")})
	refs := holdSet{ids: map[string]bool{fx.meta.ID: true}}
	if err := publishSplit(t, repo, other, refs, hooks{}); err != nil {
		t.Fatal(err)
	}
	hold, err := repo.HoldPath(fx.meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, filepath.Join(hold, dirBlocks, layer, "blk.000"), []byte("layer-body"))
	if err := publishSplit(t, repo, fx, refs, hooks{}); err != nil {
		t.Fatal(err)
	}
	layerDir, err := repo.BlocksDirPath(layer)
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, filepath.Join(layerDir, "blk.000"), []byte("layer-body"))
	assertActivePayload(t, repo, fx)
	assertGone(t, filepath.Join(hold, dirBlocks, layer))
	if _, statErr := os.Stat(hold); statErr != nil {
		t.Fatalf("referenced hold deleted: %v", statErr)
	}
}

func TestPublishReclaimsChainBlockDirFromStaleHold(t *testing.T) {
	repo := testRepo(t)
	fx, layer := seedBlocksWithChain(t, repo, []byte("layer-body"))
	other := blocksFixture([][]byte{[]byte("other-full")})
	refs := holdSet{ids: map[string]bool{fx.meta.ID: true}}
	if err := publishSplit(t, repo, other, refs, hooks{}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(repo.Dir, dirBlocks)); err != nil {
		t.Fatal(err)
	}
	writePayloadDir(t, finalPayloadDir(t, repo, fx.meta), fx)
	if err := Store(repo.MetaPath(), fx.meta); err != nil {
		t.Fatal(err)
	}
	next := []byte("image-c")
	if err := repo.Publish(next, manifestFor(next), refs); err != nil {
		t.Fatal(err)
	}
	assertBytes(t, repo.ImagePath(), next)
	hold, err := repo.HoldPath(fx.meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, filepath.Join(hold, dirBlocks, layer, "blk.000"), []byte("layer-body"))
	assertPayloadAt(t, holdPayloadPath(hold, fx.meta), fx)
	assertGone(t, filepath.Join(repo.Dir, dirBlocks, layer))
}

// TestPublishRepairsTornBlocksHold covers a crash after the whole
// _blocks/ tree reached the hold but meta.yaml is still active. The
// next publish must restore (then archive) the chain, not stick on
// "active payload missing".
func TestPublishRepairsTornBlocksHold(t *testing.T) {
	repo := testRepo(t)
	fx, layer := seedBlocksWithChain(t, repo, []byte("layer-body"))
	hold, err := repo.HoldPath(fx.meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(hold, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(repo.Dir, dirBlocks),
		filepath.Join(hold, dirBlocks)); err != nil {
		t.Fatal(err)
	}
	next := []byte("after-tear")
	if err := repo.Publish(next, manifestFor(next), nil); err != nil {
		t.Fatal(err)
	}
	assertBytes(t, repo.ImagePath(), next)
	assertGone(t, filepath.Join(repo.Dir, dirBlocks))
	assertGone(t, filepath.Join(repo.Dir, dirBlocks, layer))
	assertGone(t, filepath.Join(repo.Dir, dirHold, fx.meta.ID))
	arch, err := repo.ArchivePath(fx.meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, filepath.Join(arch, dirBlocks, layer, "blk.000"), []byte("layer-body"))
	assertPayloadAt(t, holdPayloadPath(arch, fx.meta), fx)
}

// TestPublishSweepsTornChainBlockDir covers a partial restore that
// left one chain layer directory in the active slot after the rest of
// the hold (full payload and meta) moved back. Sweep must reclaim it
// into the hold before archive, not delete it as an orphan.
func TestPublishSweepsTornChainBlockDir(t *testing.T) {
	repo := testRepo(t)
	fx, layer := seedBlocksWithChain(t, repo, []byte("layer-body"))
	hold, err := repo.HoldPath(fx.meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(hold, dirBlocks), 0o755); err != nil {
		t.Fatal(err)
	}
	fullDir, err := repo.BlocksDirPath(fx.meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(fullDir, filepath.Join(hold, dirBlocks, fx.meta.ID)); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(repo.MetaPath(), filepath.Join(hold, fileMeta)); err != nil {
		t.Fatal(err)
	}
	next := []byte("after-tear")
	if err := repo.Publish(next, manifestFor(next), nil); err != nil {
		t.Fatal(err)
	}
	assertBytes(t, repo.ImagePath(), next)
	assertGone(t, filepath.Join(repo.Dir, dirBlocks, layer))
	arch, err := repo.ArchivePath(fx.meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, filepath.Join(arch, dirBlocks, layer, "blk.000"), []byte("layer-body"))
	assertPayloadAt(t, holdPayloadPath(arch, fx.meta), fx)
}

// TestPublishSweepReturnsBranchBlockDirsByReachability covers the
// crash repair after the full blocks payload and the meta of a
// branching DAG reached the hold while both branch layer directories
// stayed in the active slot: the sweep must return every _blocks/<id>
// directory the hold manifest lists, including the branch node the
// head stack does not reach.
func TestPublishSweepReturnsBranchBlockDirsByReachability(t *testing.T) {
	repo := testRepo(t)
	manifest, layer, branch := seedBlocksWithBranch(t, repo)
	root, err := manifest.Full()
	if err != nil {
		t.Fatal(err)
	}
	hold, err := repo.HoldPath(root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(hold, dirBlocks), 0o755); err != nil {
		t.Fatal(err)
	}
	fullDir, err := repo.BlocksDirPath(root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(fullDir, filepath.Join(hold, dirBlocks, root.ID)); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(repo.MetaPath(), filepath.Join(hold, fileMeta)); err != nil {
		t.Fatal(err)
	}
	next := []byte("after-tear")
	if err := repo.Publish(next, manifestFor(next), nil); err != nil {
		t.Fatal(err)
	}
	assertBytes(t, repo.ImagePath(), next)
	assertGone(t, filepath.Join(repo.Dir, dirBlocks))
	arch, err := repo.ArchivePath(root.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, filepath.Join(arch, dirBlocks, layer, "blk.000"), []byte("layer-body"))
	assertBytes(t, filepath.Join(arch, dirBlocks, branch, "blk.000"), []byte("branch-body"))
}

// seedBlocksWithBranch publishes a blocks-form full image plus two
// branch block layer directories parented on the root, with the head
// on one branch. It returns the manifest and the two layer ids.
func seedBlocksWithBranch(t *testing.T, repo Repo) (Manifest, string, string) {
	t.Helper()
	fx := blocksFixture([][]byte{[]byte("full-a"), []byte("full-b")})
	manifest := fx.manifest()
	rootID := manifest.Head
	layerBodies := [][]byte{[]byte("layer-body")}
	branchBodies := [][]byte{[]byte("branch-body")}
	layer := blocksNodeOf(rootID, layerBodies)
	branch := blocksNodeOf(rootID, branchBodies)
	manifest = withNode(manifest, layer)
	manifest = withNode(manifest, branch)
	if err := manifest.Validate(); err != nil {
		t.Fatal(err)
	}
	writePayloadDir(t, finalPayloadDir(t, repo, fx.meta), fx)
	if err := StoreManifest(repo.MetaPath(), manifest); err != nil {
		t.Fatal(err)
	}
	writePayloadDir(t, mustBlocksDir(t, repo, layer.ID), splitFixture{
		meta: Meta{Blocks: layer.Blocks}, bodies: layerBodies,
	})
	writePayloadDir(t, mustBlocksDir(t, repo, branch.ID), splitFixture{
		meta: Meta{Blocks: branch.Blocks}, bodies: branchBodies,
	})
	return manifest, layer.ID, branch.ID
}

// blocksNodeOf builds one incremental blocks node whose block
// digests match the concatenated bodies.
func blocksNodeOf(parent string, bodies [][]byte) ImageMeta {
	entry := chainBlocksEntry(parent, bodies)
	return ImageMeta{
		ID:        entry.ID,
		Kind:      KindIncremental,
		Parent:    entry.Parent,
		Size:      entry.Size,
		SHA256:    entry.SHA256,
		MtimeUnix: entry.MtimeUnix,
		CreatedAt: entry.CreatedAt,
		Blocks:    entry.Blocks,
	}
}

// TestPublishSweepKeepsIntactActiveBranchBlockDir is the blocks twin
// of TestPublishSweepKeepsIntactActiveBranchLayer: an intact active
// branching manifest lists a branch node whose _blocks/<id> directory
// rests in the active slot, while the pinned hold lists the same id
// without holding the directory. The sweep of a later preparePublish
// must not relocate it, and the branch stack must still resolve. A
// staged split payload that mismatches its manifest runs the sweep
// and then returns before the active DAG moves aside.
func TestPublishSweepKeepsIntactActiveBranchBlockDir(t *testing.T) {
	repo := testRepo(t)
	manifest, layer, branch := seedBlocksWithBranch(t, repo)
	root, err := manifest.Full()
	if err != nil {
		t.Fatal(err)
	}
	refs := holdSet{ids: map[string]bool{root.ID: true}}
	other := blocksFixture([][]byte{[]byte("other-full")})
	if err := publishSplit(t, repo, other, refs, hooks{}); err != nil {
		t.Fatal(err)
	}
	// Republishing the branching DAG makes it active again:
	// retireStaleHold returns both layer directories into the active
	// slot, and the pinned hold survives listing them without the
	// directories.
	fx := blocksFixture([][]byte{[]byte("full-a"), []byte("full-b")})
	payloadTmp, stagedMeta := stageSplitPayload(t, repo, fx)
	removeFile(stagedMeta)
	metaTmp := stageManifest(t, repo, manifest)
	final := finalPayloadDir(t, repo, fx.meta)
	err = publishStagedPayload(repo, manifest, refs, payloadTmp, final, metaTmp, hooks{})
	if err != nil {
		t.Fatal(err)
	}
	hold, err := repo.HoldPath(root.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertGone(t, filepath.Join(hold, dirBlocks, layer))
	next := blocksFixture([][]byte{[]byte("mismatched-a")})
	corrupt := splitFixture{meta: next.meta,
		bodies: [][]byte{[]byte("torn-write")}}
	tornTmp, tornMeta := stageSplitPayload(t, repo, corrupt)
	err = publishStagedPayload(repo, next.manifest(), refs, tornTmp,
		finalPayloadDir(t, repo, next.meta), tornMeta, hooks{})
	if err == nil || !strings.Contains(err.Error(), "payload does not match meta") {
		t.Fatalf("err = %v", err)
	}
	assertBytes(t, filepath.Join(repo.Dir, dirBlocks, layer, "blk.000"), []byte("layer-body"))
	assertBytes(t, filepath.Join(repo.Dir, dirBlocks, branch, "blk.000"), []byte("branch-body"))
	assertGone(t, filepath.Join(hold, dirBlocks, layer))
	active, err := LoadManifest(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	target, err := resolveBlocksAt(repo.Dir, repo.Name(), active, layer)
	if err != nil {
		t.Fatalf("branch stack does not resolve: %v", err)
	}
	if target.Tip != layer || len(target.Members) != 2 {
		t.Fatalf("branch target = %+v", target)
	}
	assertNoTemps(t, repo.Dir)
}

// TestPublishReclaimsHoldBlockDirsWithoutManifest covers the split
// form of the reclaim fallback through a full republish: the hold of
// an id that becomes active again still owns the only copy of a
// chain block directory, but its manifest is gone. The hold then
// claims every directory resting in its _blocks/, so the next publish
// reclaims the layer directory into the active slot and it travels
// into the fresh hold instead of being deleted with the stale one.
func TestPublishReclaimsHoldBlockDirsWithoutManifest(t *testing.T) {
	repo := testRepo(t)
	fx, layer := seedBlocksWithChain(t, repo, []byte("layer-body"))
	other := blocksFixture([][]byte{[]byte("other-full")})
	refs := holdSet{ids: map[string]bool{fx.meta.ID: true}}
	if err := publishSplit(t, repo, other, refs, hooks{}); err != nil {
		t.Fatal(err)
	}
	hold, err := repo.HoldPath(fx.meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a crash that restored the full payload as active
	// without retiring the stale hold, then tear the hold manifest
	// away: the only copy of the layer directory rests in a hold
	// that cannot be listed through its manifest.
	if err := os.RemoveAll(filepath.Join(repo.Dir, dirBlocks)); err != nil {
		t.Fatal(err)
	}
	writePayloadDir(t, finalPayloadDir(t, repo, fx.meta), fx)
	if err := Store(repo.MetaPath(), fx.meta); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(hold, fileMeta)); err != nil {
		t.Fatal(err)
	}
	next := []byte("image-c")
	if err := repo.Publish(next, manifestFor(next), refs); err != nil {
		t.Fatal(err)
	}
	assertBytes(t, repo.ImagePath(), next)
	assertBytes(t, filepath.Join(hold, dirBlocks, layer, "blk.000"), []byte("layer-body"))
	assertPayloadAt(t, holdPayloadPath(hold, fx.meta), fx)
	assertGone(t, filepath.Join(repo.Dir, dirBlocks, layer))
	assertNoTemps(t, repo.Dir)
}

// TestMoveActiveToHoldReclaimsBlockDirsFromCorruptManifest is the
// split twin of TestMoveActiveToHoldReclaimsFromCorruptHoldManifest:
// a corrupt hold manifest still claims every _blocks/<id> directory
// resting in the hold, so the reclaim saves the layer directory from
// the removal of the stale hold. The republish flow cannot carry this
// case because the torn-slot repair refuses a corrupt hold manifest
// earlier, so the test drives moveActiveToHold directly.
func TestMoveActiveToHoldReclaimsBlockDirsFromCorruptManifest(t *testing.T) {
	repo := testRepo(t)
	fx, layer := seedBlocksWithChain(t, repo, []byte("layer-body"))
	manifest := fx.manifest()
	if err := moveActiveToHold(repo, manifest, hooks{}); err != nil {
		t.Fatal(err)
	}
	hold, err := repo.HoldPath(fx.meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, filepath.Join(hold, dirBlocks, layer, "blk.000"), []byte("layer-body"))
	if err := os.WriteFile(filepath.Join(hold, fileMeta), []byte("meta: [torn"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash that restored the full payload as active
	// without the layer directory: the hold owns the only copy
	// behind a corrupt manifest.
	writePayloadDir(t, finalPayloadDir(t, repo, fx.meta), fx)
	if err := StoreManifest(repo.MetaPath(), manifest); err != nil {
		t.Fatal(err)
	}
	if err := moveActiveToHold(repo, manifest, hooks{}); err != nil {
		t.Fatal(err)
	}
	assertBytes(t, filepath.Join(hold, dirBlocks, layer, "blk.000"), []byte("layer-body"))
	assertPayloadAt(t, holdPayloadPath(hold, fx.meta), fx)
	assertGone(t, filepath.Join(repo.Dir, dirBlocks, layer))
	held, err := LoadManifest(filepath.Join(hold, fileMeta))
	if err != nil {
		t.Fatalf("corrupt manifest was not replaced: %v", err)
	}
	if !held.Has(layer) {
		t.Fatalf("held manifest = %+v", held)
	}
}

func TestRepairPayloadWithoutMeta(t *testing.T) {
	cases := []struct {
		name string
		fx   splitFixture
	}{
		{"blocks", blocksFixture([][]byte{[]byte("torn-a"), []byte("torn-b")})},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/dropped", func(t *testing.T) {
			repo := testRepo(t)
			writePayloadDir(t, finalPayloadDir(t, repo, tc.fx.meta), tc.fx)
			if err := repairTornActive(repo, hooks{}, ""); err != nil {
				t.Fatal(err)
			}
			there, err := anyPayloadExists(repo.Dir)
			if err != nil {
				t.Fatal(err)
			}
			if there {
				t.Fatal("payload survived the repair")
			}
		})
		t.Run(tc.name+"/restored-from-partial-hold", func(t *testing.T) {
			repo := testRepo(t)
			writePayloadDir(t, finalPayloadDir(t, repo, tc.fx.meta), tc.fx)
			hold, err := repo.HoldPath(tc.fx.meta.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(hold, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := Store(filepath.Join(hold, fileMeta), tc.fx.meta); err != nil {
				t.Fatal(err)
			}
			if err := repairTornActive(repo, hooks{}, ""); err != nil {
				t.Fatal(err)
			}
			assertActivePayload(t, repo, tc.fx)
			assertGone(t, hold)
		})
	}
}

func TestRepairMetaWithoutPayload(t *testing.T) {
	cases := []struct {
		name string
		fx   splitFixture
	}{
		{"blocks", blocksFixture([][]byte{[]byte("lost-a")})},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/restored-from-hold", func(t *testing.T) {
			repo := testRepo(t)
			if err := os.MkdirAll(repo.Dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := Store(repo.MetaPath(), tc.fx.meta); err != nil {
				t.Fatal(err)
			}
			hold, err := repo.HoldPath(tc.fx.meta.ID)
			if err != nil {
				t.Fatal(err)
			}
			writePayloadDir(t, holdPayloadPath(hold, tc.fx.meta), tc.fx)
			if err := repairTornActive(repo, hooks{}, ""); err != nil {
				t.Fatal(err)
			}
			assertActivePayload(t, repo, tc.fx)
			assertGone(t, hold)
		})
		t.Run(tc.name+"/meta-dropped", func(t *testing.T) {
			repo := testRepo(t)
			if err := os.MkdirAll(repo.Dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := Store(repo.MetaPath(), tc.fx.meta); err != nil {
				t.Fatal(err)
			}
			if err := repairTornActive(repo, hooks{}, ""); err != nil {
				t.Fatal(err)
			}
			if _, exists, err := loadActive(repo); err != nil || exists {
				t.Fatalf("loadActive = %v %v", exists, err)
			}
		})
	}
}

func TestRepairMismatchRestoresMatchingHoldMeta(t *testing.T) {
	for _, tc := range splitKindCases() {
		t.Run(tc.name, func(t *testing.T) {
			repo := testRepo(t)
			stored := tc.build([][]byte{[]byte("stored-a"), []byte("stored-b")})
			writePayloadDir(t, finalPayloadDir(t, repo, stored.meta), stored)
			stale := tc.build([][]byte{[]byte("stale-a"), []byte("stale-b")})
			if err := os.MkdirAll(repo.Dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := Store(repo.MetaPath(), stale.meta); err != nil {
				t.Fatal(err)
			}
			hold, err := repo.HoldPath(stored.meta.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(hold, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := Store(filepath.Join(hold, fileMeta), stored.meta); err != nil {
				t.Fatal(err)
			}
			if err := repairTornActive(repo, hooks{}, ""); err != nil {
				t.Fatal(err)
			}
			assertActivePayload(t, repo, stored)
			assertGone(t, hold)
		})
	}
}

func TestRepairMismatchWithoutMatchingHoldFails(t *testing.T) {
	for _, tc := range splitKindCases() {
		t.Run(tc.name, func(t *testing.T) {
			repo := testRepo(t)
			stored := tc.build([][]byte{[]byte("stored-a"), []byte("stored-b")})
			writePayloadDir(t, finalPayloadDir(t, repo, stored.meta), stored)
			stale := tc.build([][]byte{[]byte("stale-a"), []byte("stale-b")})
			foreign := tc.build([][]byte{[]byte("foreign-a"), []byte("foreign-b")})
			if err := os.MkdirAll(repo.Dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := Store(repo.MetaPath(), stale.meta); err != nil {
				t.Fatal(err)
			}
			hold, err := repo.HoldPath(foreign.meta.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(hold, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := Store(filepath.Join(hold, fileMeta), foreign.meta); err != nil {
				t.Fatal(err)
			}
			err = repairTornActive(repo, hooks{}, "")
			if err == nil || !strings.Contains(err.Error(), "does not match") {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

// splitKindCases builds blocks-form fixtures for the repair tests.
func splitKindCases() []struct {
	name  string
	build func([][]byte) splitFixture
} {
	return []struct {
		name  string
		build func([][]byte) splitFixture
	}{
		{"blocks", blocksFixture},
	}
}

func TestRepairSweepsStrayPayloadTemps(t *testing.T) {
	repo := testRepo(t)
	if err := os.MkdirAll(repo.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	strays := []string{"._blocks.tmp.111.aa"}
	for _, name := range strays {
		dir := filepath.Join(repo.Dir, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "blk.000"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A staging directory of a publish in progress survives the
	// sweep when it is named as the keep path.
	keepName, err := tempBlocksDirName()
	if err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(repo.Dir, keepName)
	if err := os.MkdirAll(keep, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := repairTornActive(repo, hooks{}, keep); err != nil {
		t.Fatal(err)
	}
	for _, name := range strays {
		assertGone(t, filepath.Join(repo.Dir, name))
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("keep staging swept: %v", err)
	}
	if err := os.RemoveAll(keep); err != nil {
		t.Fatal(err)
	}
}

func TestLoadActiveSplitPayloads(t *testing.T) {
	t.Run("blocks", func(t *testing.T) {
		repo := testRepo(t)
		fx := blocksFixture([][]byte{[]byte("load-a"), []byte("load-b")})
		seedSplitActive(t, repo, fx)
		loaded, exists, err := loadActive(repo)
		if err != nil || !exists {
			t.Fatalf("loadActive = %v %v", exists, err)
		}
		if PayloadKind(loaded) != PayloadBlocks || loaded.ID != fx.meta.ID {
			t.Fatalf("loaded = %+v", loaded)
		}
		if err := os.RemoveAll(filepath.Join(repo.Dir, dirBlocks)); err != nil {
			t.Fatal(err)
		}
		_, _, err = loadActive(repo)
		if err == nil || !strings.Contains(err.Error(), "active payload missing") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("payload without meta", func(t *testing.T) {
		repo := testRepo(t)
		fx := blocksFixture([][]byte{[]byte("orphan-a")})
		writePayloadDir(t, finalPayloadDir(t, repo, fx.meta), fx)
		if _, _, err := loadActive(repo); !errors.Is(err, ErrMetaMissing) {
			t.Fatalf("err = %v", err)
		}
	})
}

// blocksFixture derives a valid blocks meta whose whole digest is
// the sha256 of the concatenated block bodies.
func blocksFixture(bodies [][]byte) splitFixture {
	meta := validMeta()
	meta.Blocks = nil
	var all []byte
	for i, body := range bodies {
		sum := sha256.Sum256(body)
		meta.Blocks = append(meta.Blocks, BlockEntry{
			Name:      fmt.Sprintf(blockNameFmt, i),
			Size:      int64(len(body)),
			SHA256:    hex.EncodeToString(sum[:]),
			MtimeUnix: int64(1710000000 + int64(i)),
		})
		all = append(all, body...)
	}
	whole := sha256.Sum256(all)
	meta.SHA256 = hex.EncodeToString(whole[:])
	meta.ID = ID(whole)
	meta.Size = int64(len(all))
	return splitFixture{meta: meta, bodies: bodies}
}

// manifestNames lists the payload file names of a fixture in the
// order its bodies are stored.
func manifestNames(meta Meta) []string {
	names := make([]string, len(meta.Blocks))
	for i, entry := range meta.Blocks {
		names[i] = entry.Name
	}
	return names
}

// manifestMtimes lists the recorded modification times in manifest
// order, parallel to manifestNames.
func manifestMtimes(meta Meta) []int64 {
	out := make([]int64, len(meta.Blocks))
	for i, entry := range meta.Blocks {
		out[i] = entry.MtimeUnix
	}
	return out
}

// writePayloadDir lays out the flat payload files of a fixture under
// dir, which is a staged scratch directory, an active payload
// directory, or the matching place inside a hold. The file mtimes are
// aligned with the manifest, so the seeded payload is as consistent
// as a real build.
func writePayloadDir(t *testing.T, dir string, fx splitFixture) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	mtimes := manifestMtimes(fx.meta)
	for i, name := range manifestNames(fx.meta) {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, fx.bodies[i], 0o644); err != nil {
			t.Fatal(err)
		}
		stamp := time.Unix(mtimes[i], 0)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
}

// seedBlocksWithChain publishes a blocks full image plus one chain
// layer directory _blocks/<id>/, and returns the layer id.
func seedBlocksWithChain(t *testing.T, repo Repo, layerBody []byte) (splitFixture, string) {
	t.Helper()
	fx := blocksFixture([][]byte{[]byte("full-a"), []byte("full-b")})
	const layer = "fedcba9876543210"
	if fx.meta.ID == layer {
		t.Fatal("fixture id collided with the chain layer id")
	}
	entry := validEntry(fx.meta.ID, layer)
	entry.Blocks = blocksSeq(1)
	fx.meta.Chain = []ChainEntry{entry}
	if err := fx.meta.Validate(); err != nil {
		t.Fatal(err)
	}
	seedSplitActive(t, repo, fx)
	dir, err := repo.BlocksDirPath(entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "blk.000"), layerBody, 0o644); err != nil {
		t.Fatal(err)
	}
	return fx, entry.ID
}

func seedSplitActive(t *testing.T, repo Repo, fx splitFixture) {
	t.Helper()
	writePayloadDir(t, finalPayloadDir(t, repo, fx.meta), fx)
	if err := StoreManifest(repo.MetaPath(), fx.manifest()); err != nil {
		t.Fatal(err)
	}
}

func finalPayloadDir(t *testing.T, repo Repo, meta Meta) string {
	t.Helper()
	dir, err := repo.BlocksDirPath(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// stageSplitPayload mimics a split build ready to publish: a scratch
// payload directory and a synced staged manifest inside repo.Dir.
func stageSplitPayload(t *testing.T, repo Repo, fx splitFixture) (string, string) {
	t.Helper()
	if err := os.MkdirAll(repo.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	name, err := tempBlocksDirName()
	if err != nil {
		t.Fatal(err)
	}
	payloadTmp := filepath.Join(repo.Dir, name)
	writePayloadDir(t, payloadTmp, fx)
	return payloadTmp, stageManifest(t, repo, fx.manifest())
}

func stageManifest(t *testing.T, repo Repo, manifest Manifest) string {
	t.Helper()
	body, err := EncodeManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	metaTmp, err := writeTemp(repo.Dir, fileMeta, body, hooks{})
	if err != nil {
		t.Fatal(err)
	}
	return metaTmp
}

func publishSplit(t *testing.T, repo Repo, fx splitFixture, refs Refs, seam hooks) error {
	t.Helper()
	payloadTmp, metaTmp := stageSplitPayload(t, repo, fx)
	return publishStagedPayload(repo, fx.manifest(), refs, payloadTmp,
		finalPayloadDir(t, repo, fx.meta), metaTmp, seam)
}

// assertActivePayload checks the active slot through loadActive and
// re-reads every payload file body.
func assertActivePayload(t *testing.T, repo Repo, fx splitFixture) {
	t.Helper()
	loaded, exists, err := loadActive(repo)
	if err != nil || !exists {
		t.Fatalf("loadActive = %v %v", exists, err)
	}
	if loaded.ID != fx.meta.ID {
		t.Fatalf("active id = %s, want %s", loaded.ID, fx.meta.ID)
	}
	assertPayloadAt(t, finalPayloadDir(t, repo, loaded), fx)
}

func assertPayloadAt(t *testing.T, dir string, fx splitFixture) {
	t.Helper()
	for i, name := range manifestNames(fx.meta) {
		assertBytes(t, filepath.Join(dir, name), fx.bodies[i])
	}
}

func assertGone(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s still present: %v", path, err)
	}
}
