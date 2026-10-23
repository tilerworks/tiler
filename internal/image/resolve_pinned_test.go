package image

import (
	"os"
	"path/filepath"
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
	if got.Form != PayloadBlocks || got.ID != held.meta.ID {
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
