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

func TestResolveMountBlocksForm(t *testing.T) {
	cfg := buildConfig(t)
	fx := blocksFixture([][]byte{[]byte("resolve-a"), []byte("resolve-bb")})
	repo := mustRepo(t, cfg)
	seedSplitActive(t, repo, fx)
	got, err := ResolveMount(cfg, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Library || got.Name != "alice/app" || got.ID != fx.id() ||
		got.Tip != fx.id() || got.Format != FormatSquashfs {
		t.Fatalf("target identity = %+v", got)
	}
	if got.Form != PayloadBlocks {
		t.Fatalf("form = %q", got.Form)
	}
	if got.Size != fx.root().Size || got.SHA256 != fx.root().SHA256 {
		t.Fatalf("whole facts = %d %s", got.Size, got.SHA256)
	}
	if len(got.Members) != 1 || got.Members[0].ID != fx.id() {
		t.Fatalf("stack members = %+v, want one full layer", got.Members)
	}
	if len(got.Blocks) != len(fx.root().Blocks) {
		t.Fatalf("blocks = %+v", got.Blocks)
	}
	for i, entry := range fx.root().Blocks {
		want, err := repo.BlockPath(fx.id(), entry.Name)
		if err != nil {
			t.Fatal(err)
		}
		block := got.Blocks[i]
		if block.Path != want || block.Name != entry.Name ||
			block.Size != entry.Size || block.MtimeUnix != entry.MtimeUnix ||
			block.SHA256 != entry.SHA256 {
			t.Fatalf("blocks[%d] = %+v", i, block)
		}
		if got.Members[0].Blocks[i] != block {
			t.Fatalf("members[0].blocks[%d] = %+v", i, got.Members[0].Blocks[i])
		}
	}
}

func TestResolveMountBlocksMissingFileNamesEntry(t *testing.T) {
	cfg := buildConfig(t)
	fx := blocksFixture([][]byte{[]byte("resolve-a"), []byte("resolve-bb")})
	repo := mustRepo(t, cfg)
	seedSplitActive(t, repo, fx)
	missing, err := repo.BlockPath(fx.id(), "blk.001")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(missing); err != nil {
		t.Fatal(err)
	}
	_, err = ResolveMount(cfg, "alice/app")
	if err == nil || !strings.Contains(err.Error(), "blk.001") {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveMountBlocksWithChainExpandsStack(t *testing.T) {
	cfg := buildConfig(t)
	fx := blocksFixture([][]byte{[]byte("full-a"), []byte("full-bb")})
	repo := mustRepo(t, cfg)
	layerBodies := [][]byte{[]byte("incr-a"), []byte("incr-bb")}
	node := chainBlocksNode(fx.id(), layerBodies)
	fx.manifest = withNode(fx.manifest, node)
	seedSplitActive(t, repo, fx)
	writePayloadDir(t, mustBlocksDir(t, repo, node.ID),
		blocksOnlyFixture(node.Blocks, layerBodies))

	got, err := ResolveMount(cfg, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Members) != 2 {
		t.Fatalf("members = %+v, want full + chain", got.Members)
	}
	if got.ID != fx.id() || got.Tip != node.ID {
		t.Fatalf("target ids = (%q, %q), want (%q, %q)",
			got.ID, got.Tip, fx.id(), node.ID)
	}
	if got.Members[0].ID != fx.id() || len(got.Members[0].Blocks) != 2 {
		t.Fatalf("full layer = %+v", got.Members[0])
	}
	if got.Members[1].ID != node.ID || got.Members[1].SHA256 != node.SHA256 ||
		len(got.Members[1].Blocks) != 2 {
		t.Fatalf("chain layer = %+v", got.Members[1])
	}
	want, err := repo.BlockPath(node.ID, "blk.001")
	if err != nil {
		t.Fatal(err)
	}
	if got.Members[1].Blocks[1].Path != want {
		t.Fatalf("chain block path = %s, want %s", got.Members[1].Blocks[1].Path, want)
	}
}

func TestResolveMountBlocksChainMissingFileNamesLayer(t *testing.T) {
	cfg := buildConfig(t)
	fx := blocksFixture([][]byte{[]byte("full-a")})
	repo := mustRepo(t, cfg)
	layerBodies := [][]byte{[]byte("incr-only")}
	node := chainBlocksNode(fx.id(), layerBodies)
	fx.manifest = withNode(fx.manifest, node)
	seedSplitActive(t, repo, fx)
	writePayloadDir(t, mustBlocksDir(t, repo, node.ID),
		blocksOnlyFixture(node.Blocks, layerBodies))
	missing, err := repo.BlockPath(node.ID, "blk.000")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(missing); err != nil {
		t.Fatal(err)
	}
	_, err = ResolveMount(cfg, "alice/app")
	if err == nil || !strings.Contains(err.Error(), node.ID) ||
		!strings.Contains(err.Error(), "blk.000") {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveBlocksChainMissingBlocksManifest(t *testing.T) {
	// Validate rejects this layout at LoadManifest; resolveBlocks is
	// the mount-time guard for a hand-edited or corrupt manifest that
	// somehow reaches resolution with a blocks root and a layer node
	// whose Blocks list is empty.
	cfg := buildConfig(t)
	fx := blocksFixture([][]byte{[]byte("full-a")})
	repo := mustRepo(t, cfg)
	seedSplitActive(t, repo, fx)
	layer := ImageMeta{
		ID:        "abcdef0123456789",
		Kind:      KindIncremental,
		Parent:    fx.id(),
		Size:      9,
		SHA256:    strings.Repeat("ab", 32),
		MtimeUnix: 1710000002,
		CreatedAt: time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC),
	}
	manifest := Manifest{
		Version: ManifestVersion,
		Name:    "alice/app",
		Format:  fx.manifest.Format,
		Head:    layer.ID,
		Images:  []ImageMeta{fx.root(), layer},
	}

	_, err := resolveBlocks(repo, "alice/app", manifest)
	if err == nil || !strings.Contains(err.Error(), layer.ID) ||
		!strings.Contains(err.Error(), "blocks form requires a blocks manifest") {
		t.Fatalf("err = %v", err)
	}
}

// chainBlocksNode builds one incremental blocks node whose Blocks
// digest the concatenated bodies, matching a real blocks incr
// publish.
func chainBlocksNode(parent string, bodies [][]byte) ImageMeta {
	var all []byte
	node := ImageMeta{
		Kind:      KindIncremental,
		Parent:    parent,
		MtimeUnix: 1710000002,
		CreatedAt: time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC),
	}
	for i, body := range bodies {
		sum := sha256.Sum256(body)
		node.Blocks = append(node.Blocks, BlockEntry{
			Name:      fmt.Sprintf(blockNameFmt, i),
			Size:      int64(len(body)),
			SHA256:    hex.EncodeToString(sum[:]),
			MtimeUnix: node.MtimeUnix + int64(i),
		})
		all = append(all, body...)
	}
	whole := sha256.Sum256(all)
	node.SHA256 = hex.EncodeToString(whole[:])
	node.ID = ID(whole)
	node.Size = int64(len(all))
	return node
}

// mustBlocksDir returns _blocks/<id>/ or fails the test.
func mustBlocksDir(t *testing.T, repo Repo, id string) string {
	t.Helper()
	dir, err := repo.BlocksDirPath(id)
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestResolveMountSplitArchivesNeverParticipate(t *testing.T) {
	cfg := buildConfig(t)
	fx := blocksFixture([][]byte{[]byte("archived-a")})
	repo := mustRepo(t, cfg)
	arch, err := repo.ArchivePath(fx.id())
	if err != nil {
		t.Fatal(err)
	}
	writePayloadDir(t, holdPayload(t, arch, fx.manifest), fx)
	if err := StoreManifest(filepath.Join(arch, fileMeta), fx.manifest); err != nil {
		t.Fatal(err)
	}
	_, err = ResolveMount(cfg, "alice/app")
	if !errors.Is(err, ErrUnknownImage) {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveMountSingleFileKeepsSingleForm(t *testing.T) {
	cfg := buildConfig(t)
	repo := mustRepo(t, cfg)
	manifest := seedChain(t, repo, 0)
	root := rootOf(manifest)
	got, err := ResolveMount(cfg, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	if got.Form != "" {
		t.Fatalf("form = %q for a single-file stack", got.Form)
	}
	assertTarget(t, got, Target{
		Library: true,
		Name:    "alice/app",
		ID:      root.ID,
		Tip:     root.ID,
		Format:  FormatSquashfs,
		Members: []Member{{
			Path:      repo.ImagePath(),
			ID:        root.ID,
			Size:      root.Size,
			MtimeUnix: root.MtimeUnix,
			SHA256:    root.SHA256,
		}},
	})
}

func TestQuickCheckAndVerifySplitTargets(t *testing.T) {
	t.Run("blocks", func(t *testing.T) {
		cfg := buildConfig(t)
		fx := blocksFixture([][]byte{[]byte("verify-a"), []byte("verify-bb")})
		repo := mustRepo(t, cfg)
		seedSplitActive(t, repo, fx)
		target, err := ResolveMount(cfg, "alice/app")
		if err != nil {
			t.Fatal(err)
		}
		if err := QuickCheckTarget(target); err != nil {
			t.Fatal(err)
		}
		if err := VerifyTarget(target); err != nil {
			t.Fatal(err)
		}
		old := time.Unix(1000000000, 0)
		if err := os.Chtimes(target.Blocks[1].Path, old, old); err != nil {
			t.Fatal(err)
		}
		err = QuickCheckTarget(target)
		if err == nil || !strings.Contains(err.Error(), "blk.001") {
			t.Fatalf("quick check err = %v", err)
		}
		if err := os.WriteFile(target.Blocks[0].Path, []byte("corrupted"), 0o644); err != nil {
			t.Fatal(err)
		}
		err = VerifyTarget(target)
		if err == nil || !strings.Contains(err.Error(), "blk.000") {
			t.Fatalf("verify err = %v", err)
		}
	})
	t.Run("single form has no split files", func(t *testing.T) {
		cfg := buildConfig(t)
		repo := mustRepo(t, cfg)
		seedChain(t, repo, 0)
		target, err := ResolveMount(cfg, "alice/app")
		if err != nil {
			t.Fatal(err)
		}
		if target.Form != "" {
			t.Fatalf("form = %q", target.Form)
		}
		if err := QuickCheckTarget(target); err != nil {
			t.Fatal(err)
		}
		if err := VerifyTarget(target); err != nil {
			t.Fatal(err)
		}
	})
}
