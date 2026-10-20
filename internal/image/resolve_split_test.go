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
	if !got.Library || got.Name != "alice/app" || got.ID != fx.meta.ID {
		t.Fatalf("target identity = %+v", got)
	}
	if got.Form != PayloadBlocks {
		t.Fatalf("form = %q", got.Form)
	}
	if got.Size != fx.meta.Size || got.SHA256 != fx.meta.SHA256 {
		t.Fatalf("whole facts = %d %s", got.Size, got.SHA256)
	}
	if len(got.Members) != 1 || got.Members[0].ID != fx.meta.ID {
		t.Fatalf("stack members = %+v, want one full layer", got.Members)
	}
	if len(got.Blocks) != len(fx.meta.Blocks) {
		t.Fatalf("blocks = %+v", got.Blocks)
	}
	for i, entry := range fx.meta.Blocks {
		want, err := repo.BlockPath(fx.meta.ID, entry.Name)
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
	missing, err := repo.BlockPath(fx.meta.ID, "blk.001")
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
	entry := chainBlocksEntry(fx.meta.ID, layerBodies)
	fx.meta.Chain = []ChainEntry{entry}
	seedSplitActive(t, repo, fx)
	writePayloadDir(t, mustBlocksDir(t, repo, entry.ID), splitFixture{
		meta: Meta{Blocks: entry.Blocks}, bodies: layerBodies,
	})

	got, err := ResolveMount(cfg, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Members) != 2 {
		t.Fatalf("members = %+v, want full + chain", got.Members)
	}
	if got.Members[0].ID != fx.meta.ID || len(got.Members[0].Blocks) != 2 {
		t.Fatalf("full layer = %+v", got.Members[0])
	}
	if got.Members[1].ID != entry.ID || got.Members[1].SHA256 != entry.SHA256 ||
		len(got.Members[1].Blocks) != 2 {
		t.Fatalf("chain layer = %+v", got.Members[1])
	}
	want, err := repo.BlockPath(entry.ID, "blk.001")
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
	entry := chainBlocksEntry(fx.meta.ID, layerBodies)
	fx.meta.Chain = []ChainEntry{entry}
	seedSplitActive(t, repo, fx)
	writePayloadDir(t, mustBlocksDir(t, repo, entry.ID), splitFixture{
		meta: Meta{Blocks: entry.Blocks}, bodies: layerBodies,
	})
	missing, err := repo.BlockPath(entry.ID, "blk.000")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(missing); err != nil {
		t.Fatal(err)
	}
	_, err = ResolveMount(cfg, "alice/app")
	if err == nil || !strings.Contains(err.Error(), entry.ID) ||
		!strings.Contains(err.Error(), "blk.000") {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveBlocksChainMissingBlocksManifest(t *testing.T) {
	// Validate rejects this layout at Load/Store; resolveBlocks is
	// the mount-time guard for a hand-edited or corrupt meta that
	// somehow reaches resolution with a blocks active and an empty
	// chain Blocks list.
	cfg := buildConfig(t)
	fx := blocksFixture([][]byte{[]byte("full-a")})
	repo := mustRepo(t, cfg)
	seedSplitActive(t, repo, fx)
	entry := ChainEntry{
		ID:        "abcdef0123456789",
		Parent:    fx.meta.ID,
		Size:      9,
		SHA256:    strings.Repeat("ab", 32),
		MtimeUnix: 1710000002,
		CreatedAt: time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC),
	}
	meta := fx.meta
	meta.Chain = []ChainEntry{entry}

	_, err := resolveBlocks(repo, "alice/app", meta)
	if err == nil || !strings.Contains(err.Error(), entry.ID) ||
		!strings.Contains(err.Error(), "blocks form requires a blocks manifest") {
		t.Fatalf("err = %v", err)
	}
}

// chainBlocksEntry builds one chain layer whose Blocks digest the
// concatenated bodies, matching a real blocks incr publish.
func chainBlocksEntry(parent string, bodies [][]byte) ChainEntry {
	var all []byte
	entry := ChainEntry{
		Parent:    parent,
		MtimeUnix: 1710000002,
		CreatedAt: time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC),
	}
	for i, body := range bodies {
		sum := sha256.Sum256(body)
		entry.Blocks = append(entry.Blocks, BlockEntry{
			Name:      fmt.Sprintf(blockNameFmt, i),
			Size:      int64(len(body)),
			SHA256:    hex.EncodeToString(sum[:]),
			MtimeUnix: entry.MtimeUnix + int64(i),
		})
		all = append(all, body...)
	}
	whole := sha256.Sum256(all)
	entry.SHA256 = hex.EncodeToString(whole[:])
	entry.ID = ID(whole)
	entry.Size = int64(len(all))
	return entry
}

// mustBlocksDir returns blocks/<id>/ or fails the test.
func mustBlocksDir(t *testing.T, repo Repo, id string) string {
	t.Helper()
	dir, err := repo.BlocksDirPath(id)
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestResolveMountMembersForm(t *testing.T) {
	cfg := buildConfig(t)
	fx := membersFixture([]string{"alpha", "beta"},
		[][]byte{[]byte("part-a"), []byte("part-bb")})
	repo := mustRepo(t, cfg)
	seedSplitActive(t, repo, fx)
	got, err := ResolveMount(cfg, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Library || got.ID != fx.meta.ID || got.Form != PayloadMembers {
		t.Fatalf("target = %+v", got)
	}
	if got.Size != fx.meta.Size || got.SHA256 != fx.meta.SHA256 {
		t.Fatalf("whole facts = %d %s", got.Size, got.SHA256)
	}
	if len(got.Members) != 0 || len(got.Blocks) != 0 {
		t.Fatalf("foreign manifests set: %+v %+v", got.Members, got.Blocks)
	}
	if len(got.Partitions) != len(fx.meta.Members) {
		t.Fatalf("partitions = %+v", got.Partitions)
	}
	for i, entry := range fx.meta.Members {
		want, err := repo.MemberPath(entry.Name)
		if err != nil {
			t.Fatal(err)
		}
		part := got.Partitions[i]
		if part.Path != want || part.Name != entry.Name ||
			part.Size != entry.Size || part.MtimeUnix != entry.MtimeUnix ||
			part.SHA256 != entry.SHA256 {
			t.Fatalf("partitions[%d] = %+v", i, part)
		}
	}
}

func TestResolveMountMembersMissingFileNamesEntry(t *testing.T) {
	cfg := buildConfig(t)
	fx := membersFixture([]string{"alpha", "beta"},
		[][]byte{[]byte("part-a"), []byte("part-bb")})
	repo := mustRepo(t, cfg)
	seedSplitActive(t, repo, fx)
	if err := os.Remove(filepath.Join(repo.MembersDirPath(), "beta"+squashExt)); err != nil {
		t.Fatal(err)
	}
	_, err := ResolveMount(cfg, "alice/app")
	if err == nil || !strings.Contains(err.Error(), "member beta") {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveMountSplitArchivesNeverParticipate(t *testing.T) {
	cfg := buildConfig(t)
	fx := blocksFixture([][]byte{[]byte("archived-a")})
	repo := mustRepo(t, cfg)
	arch, err := repo.ArchivePath(fx.meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	writePayloadDir(t, holdPayloadPath(arch, fx.meta), fx)
	if err := Store(filepath.Join(arch, fileMeta), fx.meta); err != nil {
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
	meta := seedChain(t, repo, 0)
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
		Members: []Member{{
			Path:      repo.ImagePath(),
			Size:      meta.Size,
			MtimeUnix: meta.MtimeUnix,
			SHA256:    meta.SHA256,
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
	t.Run("members", func(t *testing.T) {
		cfg := buildConfig(t)
		fx := membersFixture([]string{"alpha", "beta"},
			[][]byte{[]byte("verify-a"), []byte("verify-bb")})
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
		if err := os.Chtimes(target.Partitions[1].Path, old, old); err != nil {
			t.Fatal(err)
		}
		err = QuickCheckTarget(target)
		if err == nil || !strings.Contains(err.Error(), "member beta") {
			t.Fatalf("quick check err = %v", err)
		}
		if err := os.WriteFile(target.Partitions[0].Path, []byte("corrupted"), 0o644); err != nil {
			t.Fatal(err)
		}
		err = VerifyTarget(target)
		if err == nil || !strings.Contains(err.Error(), "member alpha") {
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
