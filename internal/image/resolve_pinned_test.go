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
	manifest := seedChain(t, repo, 1)
	root := rootOf(manifest)

	got, err := ResolvePinned(cfg, "alice/app", root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Library || got.Name != "alice/app" || got.ID != root.ID {
		t.Fatalf("target = %+v", got)
	}
	layer, err := repo.IncrPath(manifest.Images[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	assertTarget(t, got, Target{
		Library: true,
		Name:    "alice/app",
		ID:      root.ID,
		Tip:     manifest.Images[1].ID,
		Format:  FormatSquashfs,
		Members: []Member{
			{
				Path:      repo.ImagePath(),
				ID:        root.ID,
				Size:      root.Size,
				MtimeUnix: root.MtimeUnix,
				SHA256:    root.SHA256,
			},
			{
				Path:      layer,
				ID:        manifest.Images[1].ID,
				Size:      manifest.Images[1].Size,
				MtimeUnix: manifest.Images[1].MtimeUnix,
				SHA256:    manifest.Images[1].SHA256,
			},
		},
	})
	prefix := root.ID[:12]
	byPrefix, err := ResolvePinned(cfg, "alice/app", prefix)
	if err != nil {
		t.Fatal(err)
	}
	if byPrefix.ID != root.ID {
		t.Fatalf("prefix resolve id = %s, want %s", byPrefix.ID, root.ID)
	}
}

func TestResolvePinnedHoldHitThroughIndex(t *testing.T) {
	cfg := buildConfig(t)
	repo := mustRepo(t, cfg)
	heldBody := []byte("held-full")
	heldManifest := manifestFor(heldBody)
	held := rootOf(heldManifest)
	hold := seedPinnedHold(t, repo, heldManifest, heldBody)
	active := seedChain(t, repo, 0)
	if rootID(active) == held.ID {
		t.Fatal("active and hold ids collided")
	}
	// Length is neither 16 nor 64, so onDiskID is false and HoldPath
	// fallback cannot apply. An index miss fails the test.
	prefix := held.ID[:12]
	if onDiskID(prefix) || strings.HasPrefix(rootID(active), prefix) {
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
	if mount.ID != rootID(active) || mount.Members[0].Path != repo.ImagePath() {
		t.Fatalf("ResolveMount = %+v, want active %s", mount, rootID(active))
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
	node := chainBlocksNode(held.id(), layerBodies)
	held.manifest = withNode(held.manifest, node)
	hold := seedPinnedBlocksHold(t, repo, held, layerBodies)

	active := blocksFixture([][]byte{[]byte("new-active")})
	if active.id() == held.id() {
		t.Fatal("active and hold ids collided")
	}
	seedSplitActive(t, repo, active)

	got, err := ResolvePinned(cfg, "alice/app", held.id())
	if err != nil {
		t.Fatal(err)
	}
	if got.Form != PayloadBlocks || got.ID != held.id() || got.Tip != node.ID {
		t.Fatalf("target = %+v", got)
	}
	wantBlocksDir := filepath.Join(hold, dirBlocks, held.id())
	if got.BlocksDir != wantBlocksDir {
		t.Fatalf("BlocksDir = %s, want %s", got.BlocksDir, wantBlocksDir)
	}
	holdMarker := filepath.Join(dirHold, held.id(), dirBlocks)
	for _, member := range got.Members {
		for _, block := range member.Blocks {
			if !strings.Contains(block.Path, holdMarker) {
				t.Fatalf("block path %s not under hold _blocks", block.Path)
			}
		}
	}
	wantFull, err := blockPathAt(hold, held.id(), "blk.000")
	if err != nil {
		t.Fatal(err)
	}
	if got.Members[0].Blocks[0].Path != wantFull {
		t.Fatalf("full block = %s, want %s", got.Members[0].Blocks[0].Path, wantFull)
	}
	wantLayer, err := blockPathAt(hold, node.ID, "blk.000")
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
	if mount.ID != active.id() {
		t.Fatalf("ResolveMount id = %s, want active", mount.ID)
	}
	activeBlock, err := repo.BlockPath(active.id(), "blk.000")
	if err != nil {
		t.Fatal(err)
	}
	if mount.Blocks[0].Path != activeBlock {
		t.Fatalf("ResolveMount still uses active blocks: %s", mount.Blocks[0].Path)
	}
}

// seedPinnedHold writes a single-file hold with manifest and payload.
func seedPinnedHold(t *testing.T, repo Repo, manifest Manifest, body []byte) string {
	t.Helper()
	hold, err := repo.HoldPath(rootID(manifest))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(hold, dirIncr), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(hold, activeImageName(manifest.Format.Type))
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := StoreManifest(filepath.Join(hold, fileMeta), manifest); err != nil {
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
	base := manifestFor(body)
	manifest := Manifest{
		Version: ManifestVersion,
		Name:    base.Name,
		Format:  base.Format,
		Head:    head,
		Images:  append([]ImageMeta{rootOf(base)}, nodes...),
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
		ID:        node.ID,
		Size:      node.Size,
		MtimeUnix: node.MtimeUnix,
		SHA256:    node.SHA256,
	}
}

func TestResolveStackTipActiveBranchTip(t *testing.T) {
	cfg := buildConfig(t)
	repo := mustRepo(t, cfg)
	body := []byte("tip-full")
	root := rootOf(manifestFor(body))
	layerA := layerNodeOf(root.ID, "aaaaaaaaaaaaaaaa")
	layerB := layerNodeOf(layerA.ID, "bbbbbbbbbbbbbbbb")
	layerC := layerNodeOf(layerA.ID, "cccccccccccccccc")
	// The head moved to C, so B is a branch tip off the head path.
	manifest := branchManifest(t, body, layerC.ID, layerA, layerB, layerC)
	seedActiveManifest(t, repo, manifest)

	got, err := ResolveStackTip(cfg, "alice/app", root.ID, layerB.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertTarget(t, got, Target{
		Library: true,
		Name:    "alice/app",
		ID:      root.ID,
		Tip:     layerB.ID,
		Format:  FormatSquashfs,
		Members: []Member{
			{
				Path:      repo.ImagePath(),
				ID:        root.ID,
				Size:      root.Size,
				MtimeUnix: root.MtimeUnix,
				SHA256:    root.SHA256,
			},
			layerMember(repo.Dir, layerA),
			layerMember(repo.Dir, layerB),
		},
	})

	// The full id is its own tip: the stack holds the root alone.
	fullOnly, err := ResolveStackTip(cfg, "alice/app", root.ID, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fullOnly.ID != root.ID || fullOnly.Tip != root.ID || len(fullOnly.Members) != 1 {
		t.Fatalf("full-only target = %+v", fullOnly)
	}

	// The name-only mount keeps serving the head branch.
	mount, err := ResolveMount(cfg, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	if mount.ID != root.ID || mount.Tip != layerC.ID || len(mount.Members) != 3 {
		t.Fatalf("ResolveMount = %+v, want the head stack rooted at %s", mount, root.ID)
	}
}

func TestResolveStackTipBlocksBranchTip(t *testing.T) {
	cfg := buildConfig(t)
	repo := mustRepo(t, cfg)
	fx := blocksFixture([][]byte{[]byte("tip-full-a"), []byte("tip-full-bb")})
	bodiesA := [][]byte{[]byte("tip-incr-a")}
	bodiesB := [][]byte{[]byte("tip-incr-b")}
	bodiesC := [][]byte{[]byte("tip-incr-c")}
	nodeA := chainBlocksNode(fx.id(), bodiesA)
	nodeB := chainBlocksNode(nodeA.ID, bodiesB)
	nodeC := chainBlocksNode(nodeA.ID, bodiesC)
	// The head moved to C, so B is a branch tip off the head path.
	manifest := blocksBranchManifest(t, fx, nodeC.ID, nodeA, nodeB, nodeC)
	seedActiveBlocksManifest(t, repo, manifest)
	root := fx.root()
	seedNodeBlocks(t, repo, root, fx.bodies)
	seedNodeBlocks(t, repo, nodeA, bodiesA)
	seedNodeBlocks(t, repo, nodeB, bodiesB)
	seedNodeBlocks(t, repo, nodeC, bodiesC)

	got, err := ResolveStackTip(cfg, "alice/app", fx.id(), nodeB.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Form != PayloadBlocks {
		t.Fatalf("form = %q, want the blocks payload", got.Form)
	}
	wantBlocksDir := filepath.Join(repo.Dir, dirBlocks, fx.id())
	if got.BlocksDir != wantBlocksDir ||
		got.ConcatView != filepath.Join(wantBlocksDir, fileConcatView) {
		t.Fatalf("blocks location = (%q, %q), want dir %q",
			got.BlocksDir, got.ConcatView, wantBlocksDir)
	}
	if got.Size != root.Size || got.SHA256 != root.SHA256 {
		t.Fatalf("whole facts = (%d, %q), want (%d, %q)",
			got.Size, got.SHA256, root.Size, root.SHA256)
	}
	rootBlocks := nodeBlocks(t, repo.Dir, root)
	if !slices.Equal(got.Blocks, rootBlocks) {
		t.Fatalf("blocks = %+v, want the full root's %+v", got.Blocks, rootBlocks)
	}
	assertTarget(t, got, Target{
		Library: true,
		Name:    "alice/app",
		ID:      fx.id(),
		Tip:     nodeB.ID,
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
	if mount.ID != fx.id() || mount.Tip != nodeC.ID || len(mount.Members) != 3 {
		t.Fatalf("ResolveMount = %+v, want the head stack tipped at %s", mount, nodeC.ID)
	}
}

func TestResolveStackTipHoldFallback(t *testing.T) {
	cfg := buildConfig(t)
	repo := mustRepo(t, cfg)
	active := seedChain(t, repo, 0)
	body := []byte("held-full")
	held := rootOf(manifestFor(body))
	if held.ID == rootID(active) {
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
				ID:        held.ID,
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
	if mount.ID != rootID(active) || mount.Tip != rootID(active) {
		t.Fatalf("ResolveMount = (%q, %q), want active %s",
			mount.ID, mount.Tip, rootID(active))
	}
}

func TestResolveStackTipEmptyTipKeepsPinnedSemantics(t *testing.T) {
	t.Run("active leaf", func(t *testing.T) {
		cfg := buildConfig(t)
		repo := mustRepo(t, cfg)
		manifest := seedChain(t, repo, 2)
		got, err := ResolveStackTip(cfg, "alice/app", rootID(manifest), "")
		if err != nil {
			t.Fatal(err)
		}
		want, err := ResolvePinned(cfg, "alice/app", rootID(manifest))
		if err != nil {
			t.Fatal(err)
		}
		assertTarget(t, got, want)
		if got.Tip != manifest.Head {
			t.Fatalf("tip = %q, want the chain tail %q", got.Tip, manifest.Head)
		}
	})
	t.Run("hold leaf", func(t *testing.T) {
		cfg := buildConfig(t)
		repo := mustRepo(t, cfg)
		heldBody := []byte("held-full")
		heldManifest := manifestFor(heldBody)
		held := rootOf(heldManifest)
		seedPinnedHold(t, repo, heldManifest, heldBody)
		active := seedChain(t, repo, 0)
		if rootID(active) == held.ID {
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
	root := rootOf(manifestFor(body))
	layerA := layerNodeOf(root.ID, "aaaaaaaaaaaaaaaa")
	manifest := branchManifest(t, body, layerA.ID, layerA)
	seedActiveManifest(t, repo, manifest)

	// The full id matches the active root, but no leaf carries the
	// requested tip.
	unknown := strings.Repeat("e", sumHexLen)
	_, err := ResolveStackTip(cfg, "alice/app", root.ID, unknown)
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
	hold, err := repo.HoldPath(fx.id())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(hold, 0o755); err != nil {
		t.Fatal(err)
	}
	writePayloadDir(t, filepath.Join(hold, dirBlocks, fx.id()), fx)
	for i, node := range fx.manifest.Images[1:] {
		bodies := layerBodies
		if i > 0 {
			t.Fatal("seedPinnedBlocksHold supports one chain layer")
		}
		writePayloadDir(t, filepath.Join(hold, dirBlocks, node.ID),
			blocksOnlyFixture(node.Blocks, bodies))
	}
	if err := StoreManifest(filepath.Join(hold, fileMeta), fx.manifest); err != nil {
		t.Fatal(err)
	}
	return hold
}

// blocksBranchManifest builds a v2 blocks manifest: the full root of
// the fixture plus the given incremental nodes, headed by head. The
// nodes may branch; EncodeManifest validates the result, so a broken
// fixture fails the test at once.
func blocksBranchManifest(t *testing.T, fx splitFixture, head string, nodes ...ImageMeta) Manifest {
	t.Helper()
	manifest := Manifest{
		Version: ManifestVersion,
		Name:    fx.manifest.Name,
		Format:  fx.manifest.Format,
		Head:    head,
		Images:  append([]ImageMeta{fx.root()}, nodes...),
	}
	if _, err := EncodeManifest(manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
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
	writePayloadDir(t, mustBlocksDir(t, repo, node.ID),
		blocksOnlyFixture(node.Blocks, bodies))
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
