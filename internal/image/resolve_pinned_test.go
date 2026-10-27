package image

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestResolvePinnedActiveHit(t *testing.T) {
	cfg := buildConfig(t)
	repo := mustRepo(t, cfg)
	meta := seedChain(t, repo, 1)

	got, err := ResolvePinned(cfg, "alice/app", meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Library || got.Name != "alice/app" || got.ID != meta.ID {
		t.Fatalf("target = %+v", got)
	}
	layer, err := repo.IncrPath(meta.Chain[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	assertTarget(t, got, Target{
		Library: true,
		Name:    "alice/app",
		ID:      meta.ID,
		Tip:     meta.Chain[0].ID,
		Format:  FormatSquashfs,
		Members: []Member{
			{
				Path:      repo.ImagePath(),
				Size:      meta.Size,
				MtimeUnix: meta.MtimeUnix,
				SHA256:    meta.SHA256,
			},
			{
				Path:      layer,
				Size:      meta.Chain[0].Size,
				MtimeUnix: meta.Chain[0].MtimeUnix,
				SHA256:    meta.Chain[0].SHA256,
			},
		},
	})
	prefix := meta.ID[:12]
	byPrefix, err := ResolvePinned(cfg, "alice/app", prefix)
	if err != nil {
		t.Fatal(err)
	}
	if byPrefix.ID != meta.ID {
		t.Fatalf("prefix resolve id = %s, want %s", byPrefix.ID, meta.ID)
	}
}

func TestResolvePinnedHoldHitThroughIndex(t *testing.T) {
	cfg := buildConfig(t)
	repo := mustRepo(t, cfg)
	heldBody := []byte("held-full")
	held := metaFor(heldBody)
	held.Name = "alice/app"
	hold := seedPinnedHold(t, repo, held, heldBody)
	active := seedChain(t, repo, 0)
	if active.ID == held.ID {
		t.Fatal("active and hold ids collided")
	}
	// Length is neither 16 nor 64, so onDiskID is false and HoldPath
	// fallback cannot apply. An index miss fails the test.
	prefix := held.ID[:12]
	if onDiskID(prefix) || strings.HasPrefix(active.ID, prefix) {
		t.Fatalf("prefix %s is not a unique non-directory id reference", prefix)
	}

	got, err := ResolvePinned(cfg, "alice/app", prefix)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != held.ID {
		t.Fatalf("id = %s, want hold %s", got.ID, held.ID)
	}
	wantPath := filepath.Join(hold, fileImage)
	if len(got.Members) != 1 || got.Members[0].Path != wantPath {
		t.Fatalf("members = %+v, want %s", got.Members, wantPath)
	}
	if strings.Contains(got.Members[0].Path, filepath.Join(dirHold, held.ID)) == false {
		t.Fatalf("member path %s is not under hold", got.Members[0].Path)
	}

	// Name-only mount still reads the new active, not the hold.
	mount, err := ResolveMount(cfg, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	if mount.ID != active.ID || mount.Members[0].Path != repo.ImagePath() {
		t.Fatalf("ResolveMount = %+v, want active %s", mount, active.ID)
	}
}

func TestResolvePinnedMissingIDFails(t *testing.T) {
	cfg := buildConfig(t)
	repo := mustRepo(t, cfg)
	seedChain(t, repo, 0)

	_, err := ResolvePinned(cfg, "alice/app", strings.Repeat("f", sumHexLen))
	if err == nil || !strings.Contains(err.Error(), "alice/app") ||
		!strings.Contains(err.Error(), strings.Repeat("f", sumHexLen)) {
		t.Fatalf("err = %v", err)
	}
}

func TestResolvePinnedBlocksHoldPaths(t *testing.T) {
	cfg := buildConfig(t)
	repo := mustRepo(t, cfg)
	held := blocksFixture([][]byte{[]byte("held-a"), []byte("held-bb")})
	layerBodies := [][]byte{[]byte("held-incr")}
	entry := chainBlocksEntry(held.meta.ID, layerBodies)
	held.meta.Chain = []ChainEntry{entry}
	hold := seedPinnedBlocksHold(t, repo, held, layerBodies)

	active := blocksFixture([][]byte{[]byte("new-active")})
	if active.meta.ID == held.meta.ID {
		t.Fatal("active and hold ids collided")
	}
	seedSplitActive(t, repo, active)

	got, err := ResolvePinned(cfg, "alice/app", held.meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Form != PayloadBlocks || got.ID != held.meta.ID || got.Tip != entry.ID {
		t.Fatalf("target = %+v", got)
	}
	wantBlocksDir := filepath.Join(hold, dirBlocks, held.meta.ID)
	if got.BlocksDir != wantBlocksDir {
		t.Fatalf("BlocksDir = %s, want %s", got.BlocksDir, wantBlocksDir)
	}
	holdMarker := filepath.Join(dirHold, held.meta.ID, dirBlocks)
	for _, member := range got.Members {
		for _, block := range member.Blocks {
			if !strings.Contains(block.Path, holdMarker) {
				t.Fatalf("block path %s not under hold _blocks", block.Path)
			}
		}
	}
	wantFull, err := blockPathAt(hold, held.meta.ID, "blk.000")
	if err != nil {
		t.Fatal(err)
	}
	if got.Members[0].Blocks[0].Path != wantFull {
		t.Fatalf("full block = %s, want %s", got.Members[0].Blocks[0].Path, wantFull)
	}
	wantLayer, err := blockPathAt(hold, entry.ID, "blk.000")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Members) != 2 || got.Members[1].Blocks[0].Path != wantLayer {
		t.Fatalf("chain members = %+v", got.Members)
	}

	mount, err := ResolveMount(cfg, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	if mount.ID != active.meta.ID {
		t.Fatalf("ResolveMount id = %s, want active", mount.ID)
	}
	activeBlock, err := repo.BlockPath(active.meta.ID, "blk.000")
	if err != nil {
		t.Fatal(err)
	}
	if mount.Blocks[0].Path != activeBlock {
		t.Fatalf("ResolveMount still uses active blocks: %s", mount.Blocks[0].Path)
	}
}

// seedPinnedHold writes a single-file hold with meta and payload.
func seedPinnedHold(t *testing.T, repo Repo, meta Meta, body []byte) string {
	t.Helper()
	hold, err := repo.HoldPath(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(hold, dirIncr), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(hold, activeImageName(meta.Format.Type))
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Store(filepath.Join(hold, fileMeta), meta); err != nil {
		t.Fatal(err)
	}
	return hold
}

// branchManifest builds a v2 single-file manifest: the full root of
// body plus the given incremental nodes, headed by head. The nodes
// may branch; EncodeManifest validates the result, so a broken
// fixture fails the test at once.
func branchManifest(t *testing.T, body []byte, head string, nodes ...ImageMeta) Manifest {
	t.Helper()
	meta := metaFor(body)
	manifest := Manifest{
		Version: ManifestVersion,
		Name:    meta.Name,
		Format:  meta.Format,
		Head:    head,
		Images:  append([]ImageMeta{rootNodeOf(meta)}, nodes...),
	}
	if _, err := EncodeManifest(manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}

// layerMember builds the want member of one incremental node whose
// layer file rests under dir/_incr/.
func layerMember(dir string, node ImageMeta) Member {
	return Member{
		Path:      filepath.Join(dir, dirIncr, node.ID+squashExt),
		Size:      node.Size,
		MtimeUnix: node.MtimeUnix,
		SHA256:    node.SHA256,
	}
}

func TestResolveStackTipActiveBranchTip(t *testing.T) {
	cfg := buildConfig(t)
	repo := mustRepo(t, cfg)
	body := []byte("tip-full")
	meta := metaFor(body)
	layerA := layerNodeOf(meta.ID, "aaaaaaaaaaaaaaaa")
	layerB := layerNodeOf(layerA.ID, "bbbbbbbbbbbbbbbb")
	layerC := layerNodeOf(layerA.ID, "cccccccccccccccc")
	// The head moved to C, so B is a branch tip off the head path.
	manifest := branchManifest(t, body, layerC.ID, layerA, layerB, layerC)
	seedActiveManifest(t, repo, manifest)

	got, err := ResolveStackTip(cfg, "alice/app", meta.ID, layerB.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertTarget(t, got, Target{
		Library: true,
		Name:    "alice/app",
		ID:      meta.ID,
		Tip:     layerB.ID,
		Format:  FormatSquashfs,
		Members: []Member{
			{
				Path:      repo.ImagePath(),
				Size:      meta.Size,
				MtimeUnix: meta.MtimeUnix,
				SHA256:    meta.SHA256,
			},
			layerMember(repo.Dir, layerA),
			layerMember(repo.Dir, layerB),
		},
	})

	// The full id is its own tip: the stack holds the root alone.
	fullOnly, err := ResolveStackTip(cfg, "alice/app", meta.ID, meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fullOnly.ID != meta.ID || fullOnly.Tip != meta.ID || len(fullOnly.Members) != 1 {
		t.Fatalf("full-only target = %+v", fullOnly)
	}

	// The name-only mount keeps serving the head branch.
	mount, err := ResolveMount(cfg, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	if mount.ID != meta.ID || mount.Tip != layerC.ID || len(mount.Members) != 3 {
		t.Fatalf("ResolveMount = %+v, want the head stack rooted at %s", mount, meta.ID)
	}
}

func TestResolveStackTipBlocksBranchTip(t *testing.T) {
	cfg := buildConfig(t)
	repo := mustRepo(t, cfg)
	fx := blocksFixture([][]byte{[]byte("tip-full-a"), []byte("tip-full-bb")})
	bodiesA := [][]byte{[]byte("tip-incr-a")}
	bodiesB := [][]byte{[]byte("tip-incr-b")}
	bodiesC := [][]byte{[]byte("tip-incr-c")}
	entryA := chainBlocksEntry(fx.meta.ID, bodiesA)
	entryB := chainBlocksEntry(entryA.ID, bodiesB)
	entryC := chainBlocksEntry(entryA.ID, bodiesC)
	// The head moved to C, so B is a branch tip off the head path.
	manifest := blocksBranchManifest(t, fx.meta, entryC.ID, entryA, entryB, entryC)
	seedActiveBlocksManifest(t, repo, manifest)
	root := rootNodeOf(fx.meta)
	nodeA, nodeB, nodeC := chainNodeOf(entryA), chainNodeOf(entryB), chainNodeOf(entryC)
	seedNodeBlocks(t, repo, root, fx.bodies)
	seedNodeBlocks(t, repo, nodeA, bodiesA)
	seedNodeBlocks(t, repo, nodeB, bodiesB)
	seedNodeBlocks(t, repo, nodeC, bodiesC)

	got, err := ResolveStackTip(cfg, "alice/app", fx.meta.ID, entryB.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Form != PayloadBlocks {
		t.Fatalf("form = %q, want the blocks payload", got.Form)
	}
	wantBlocksDir := filepath.Join(repo.Dir, dirBlocks, fx.meta.ID)
	if got.BlocksDir != wantBlocksDir ||
		got.ConcatView != filepath.Join(wantBlocksDir, fileConcatView) {
		t.Fatalf("blocks location = (%q, %q), want dir %q",
			got.BlocksDir, got.ConcatView, wantBlocksDir)
	}
	if got.Size != fx.meta.Size || got.SHA256 != fx.meta.SHA256 {
		t.Fatalf("whole facts = (%d, %q), want (%d, %q)",
			got.Size, got.SHA256, fx.meta.Size, fx.meta.SHA256)
	}
	rootBlocks := nodeBlocks(t, repo.Dir, root)
	if !slices.Equal(got.Blocks, rootBlocks) {
		t.Fatalf("blocks = %+v, want the full root's %+v", got.Blocks, rootBlocks)
	}
	assertTarget(t, got, Target{
		Library: true,
		Name:    "alice/app",
		ID:      fx.meta.ID,
		Tip:     entryB.ID,
		Format:  FormatSquashfs,
		Members: []Member{
			blocksMemberOf(root, rootBlocks),
			blocksMemberOf(nodeA, nodeBlocks(t, repo.Dir, nodeA)),
			blocksMemberOf(nodeB, nodeBlocks(t, repo.Dir, nodeB)),
		},
	})

	// The name-only mount keeps serving the head branch.
	mount, err := ResolveMount(cfg, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	if mount.ID != fx.meta.ID || mount.Tip != entryC.ID || len(mount.Members) != 3 {
		t.Fatalf("ResolveMount = %+v, want the head stack tipped at %s", mount, entryC.ID)
	}
}

func TestResolveStackTipHoldFallback(t *testing.T) {
	cfg := buildConfig(t)
	repo := mustRepo(t, cfg)
	active := seedChain(t, repo, 0)
	body := []byte("held-full")
	held := metaFor(body)
	if held.ID == active.ID {
		t.Fatal("active and hold ids collided")
	}
	layerA := layerNodeOf(held.ID, "aaaaaaaaaaaaaaaa")
	layerB := layerNodeOf(held.ID, "bbbbbbbbbbbbbbbb")
	manifest := branchManifest(t, body, layerB.ID, layerA, layerB)
	hold := seedHoldManifest(t, repo, manifest)

	// The active manifest roots at another full id, so the pinned
	// hold leaf resolves the requested branch tip.
	got, err := ResolveStackTip(cfg, "alice/app", held.ID, layerA.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertTarget(t, got, Target{
		Library: true,
		Name:    "alice/app",
		ID:      held.ID,
		Tip:     layerA.ID,
		Format:  FormatSquashfs,
		Members: []Member{
			{
				Path:      filepath.Join(hold, fileImage),
				Size:      held.Size,
				MtimeUnix: held.MtimeUnix,
				SHA256:    held.SHA256,
			},
			layerMember(hold, layerA),
		},
	})

	// The name-only mount still reads the active head, not the hold.
	mount, err := ResolveMount(cfg, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	if mount.ID != active.ID || mount.Tip != active.ID {
		t.Fatalf("ResolveMount = (%q, %q), want active %s", mount.ID, mount.Tip, active.ID)
	}
}

func TestResolveStackTipEmptyTipKeepsPinnedSemantics(t *testing.T) {
	t.Run("active leaf", func(t *testing.T) {
		cfg := buildConfig(t)
		repo := mustRepo(t, cfg)
		meta := seedChain(t, repo, 2)
		got, err := ResolveStackTip(cfg, "alice/app", meta.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		want, err := ResolvePinned(cfg, "alice/app", meta.ID)
		if err != nil {
			t.Fatal(err)
		}
		assertTarget(t, got, want)
		if got.Tip != meta.Chain[1].ID {
			t.Fatalf("tip = %q, want the chain tail %q", got.Tip, meta.Chain[1].ID)
		}
	})
	t.Run("hold leaf", func(t *testing.T) {
		cfg := buildConfig(t)
		repo := mustRepo(t, cfg)
		heldBody := []byte("held-full")
		held := metaFor(heldBody)
		held.Name = "alice/app"
		seedPinnedHold(t, repo, held, heldBody)
		active := seedChain(t, repo, 0)
		if active.ID == held.ID {
			t.Fatal("active and hold ids collided")
		}
		got, err := ResolveStackTip(cfg, "alice/app", held.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		want, err := ResolvePinned(cfg, "alice/app", held.ID)
		if err != nil {
			t.Fatal(err)
		}
		assertTarget(t, got, want)
		if got.ID != held.ID || got.Tip != held.ID {
			t.Fatalf("ids = (%q, %q), want the hold leaf %q", got.ID, got.Tip, held.ID)
		}
	})
}

func TestResolveStackTipUnknownTipFails(t *testing.T) {
	cfg := buildConfig(t)
	repo := mustRepo(t, cfg)
	body := []byte("tip-full")
	meta := metaFor(body)
	layerA := layerNodeOf(meta.ID, "aaaaaaaaaaaaaaaa")
	manifest := branchManifest(t, body, layerA.ID, layerA)
	seedActiveManifest(t, repo, manifest)

	// The full id matches the active root, but no leaf carries the
	// requested tip.
	unknown := strings.Repeat("e", sumHexLen)
	_, err := ResolveStackTip(cfg, "alice/app", meta.ID, unknown)
	if err == nil {
		t.Fatal("want refusal, got nil error")
	}
	for _, text := range []string{"alice/app", unknown, "unknown stack tip"} {
		if !strings.Contains(err.Error(), text) {
			t.Fatalf("err = %v, want text %q", err, text)
		}
	}

	// An unknown full id fails through the pinned location rules and
	// quotes the reference.
	_, err = ResolveStackTip(cfg, "alice/app", unknown, layerA.ID)
	if err == nil || !strings.Contains(err.Error(), unknown) {
		t.Fatalf("err = %v, want the unknown id quoted", err)
	}
}

// seedPinnedBlocksHold writes a blocks hold (full + optional chain)
// under _hold/<id>/_blocks/<layer-id>/.
func seedPinnedBlocksHold(t *testing.T, repo Repo, fx splitFixture, layerBodies [][]byte) string {
	t.Helper()
	hold, err := repo.HoldPath(fx.meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(hold, 0o755); err != nil {
		t.Fatal(err)
	}
	writePayloadDir(t, filepath.Join(hold, dirBlocks, fx.meta.ID), fx)
	for i, entry := range fx.meta.Chain {
		bodies := layerBodies
		if i > 0 {
			t.Fatal("seedPinnedBlocksHold supports one chain layer")
		}
		writePayloadDir(t, filepath.Join(hold, dirBlocks, entry.ID), splitFixture{
			meta: Meta{Blocks: entry.Blocks}, bodies: bodies,
		})
	}
	if err := Store(filepath.Join(hold, fileMeta), fx.meta); err != nil {
		t.Fatal(err)
	}
	return hold
}

// blocksBranchManifest builds a v2 blocks manifest: the full root of
// meta plus the given chain entries as incremental nodes, headed by
// head. The nodes may branch; EncodeManifest validates the result, so
// a broken fixture fails the test at once.
func blocksBranchManifest(t *testing.T, meta Meta, head string, entries ...ChainEntry) Manifest {
	t.Helper()
	manifest := Manifest{
		Version: ManifestVersion,
		Name:    meta.Name,
		Format:  meta.Format,
		Head:    head,
		Images:  []ImageMeta{rootNodeOf(meta)},
	}
	for _, entry := range entries {
		manifest.Images = append(manifest.Images, chainNodeOf(entry))
	}
	if _, err := EncodeManifest(manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}

// chainNodeOf converts one v1 chain entry into the incremental
// manifest node a v2 document carries.
func chainNodeOf(entry ChainEntry) ImageMeta {
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

// seedActiveBlocksManifest writes a v2 blocks manifest into the
// active slot of repo without any payload; callers seed the byte
// blocks of the nodes they need with seedNodeBlocks. A manifest
// resting without its _blocks payload is exactly the refusal shape
// loadActiveManifest guards.
func seedActiveBlocksManifest(t *testing.T, repo Repo, manifest Manifest) {
	t.Helper()
	if err := os.MkdirAll(repo.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body, err := EncodeManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(repo.MetaPath(), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

// seedNodeBlocks writes the byte blocks of one manifest node into
// the active _blocks/<node-id>/ directory, aligned with the recorded
// block digests and mtimes.
func seedNodeBlocks(t *testing.T, repo Repo, node ImageMeta, bodies [][]byte) {
	t.Helper()
	writePayloadDir(t, mustBlocksDir(t, repo, node.ID), splitFixture{
		meta: Meta{Blocks: node.Blocks}, bodies: bodies,
	})
}

// nodeBlocks resolves the manifest blocks of one node into the
// BlockFile facts a resolved member records: absolute paths under
// root/_blocks/<node-id>/ paired with the recorded block facts.
func nodeBlocks(t *testing.T, root string, node ImageMeta) []BlockFile {
	t.Helper()
	blocks := make([]BlockFile, 0, len(node.Blocks))
	for _, entry := range node.Blocks {
		path, err := blockPathAt(root, node.ID, entry.Name)
		if err != nil {
			t.Fatal(err)
		}
		blocks = append(blocks, BlockFile{
			Path:      path,
			Name:      entry.Name,
			Size:      entry.Size,
			MtimeUnix: entry.MtimeUnix,
			SHA256:    entry.SHA256,
		})
	}
	return blocks
}

// blocksMemberOf builds the want member of one blocks-form node with
// its resolved byte blocks.
func blocksMemberOf(node ImageMeta, blocks []BlockFile) Member {
	return Member{
		ID:        node.ID,
		Size:      node.Size,
		MtimeUnix: node.MtimeUnix,
		SHA256:    node.SHA256,
		Blocks:    blocks,
	}
}
