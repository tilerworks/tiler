package image

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/config"
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
	if len(got.Members) != 0 {
		t.Fatalf("stack members set for a blocks target: %+v", got.Members)
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

func TestResolveMountSplitEnabledKeepsSingleForm(t *testing.T) {
	cfg := buildConfig(t)
	cfg.SplitEnabled = true
	cfg.SplitMode = config.SplitModeBlocks
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
