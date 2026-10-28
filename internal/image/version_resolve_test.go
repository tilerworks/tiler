package image

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jiangxin/tiler/internal/config"
)

func TestResolveVersionMountBranchesFormatsAndOwnership(t *testing.T) {
	repo := testRepo(t)
	root := publishResolveVersion(t, repo, "root", ImageMeta{}, FormatSquashfs)
	_ = publishResolveVersion(t, repo, "other-root", ImageMeta{}, FormatErofs)
	branch := publishResolveVersion(t, repo, "branch", root, FormatErofs)
	cfg := config.Config{ImagesDir: linkImagesRoot(repo)}
	got, err := ResolveVersionMount(cfg, repo.Name()+":"+branch.ID[:8])
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != root.ID || got.Tip != branch.ID || len(got.Members) != 2 ||
		got.Members[0].Format != FormatSquashfs || got.Members[1].Format != FormatErofs {
		t.Fatalf("branch target = %+v", got)
	}
	owned, err := ResolveVersionMount(cfg, got.Members[1].Path)
	if err != nil || owned.Name != repo.Name() || owned.Tip != branch.ID {
		t.Fatalf("owned target = %+v, %v", owned, err)
	}
}

func TestResolveVersionMountSplitAndColonPath(t *testing.T) {
	repo := testRepo(t)
	stage, node := stagedSplitVersion(t, "split-payload", 4)
	node.Format = Format{Type: FormatSquashfs, Compression: CompressionZstd}
	if _, err := PublishFullVersion(linkImagesRoot(repo), repo.Name(), stage, node, false); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{ImagesDir: linkImagesRoot(repo)}
	target, err := ResolveVersionMount(cfg, repo.Name())
	if err != nil || len(target.Members) != 1 || len(target.Members[0].Blocks) < 2 {
		t.Fatalf("split target = %+v, %v", target, err)
	}
	owned, err := ResolveVersionMount(cfg, target.Members[0].Blocks[0].Path)
	if err != nil || !owned.Library || owned.Tip != node.ID {
		t.Fatalf("split owner = %+v, %v", owned, err)
	}
	external := filepath.Join(t.TempDir(), "payload:with:colon.sqsh")
	if err := os.WriteFile(external, []byte("external"), 0o644); err != nil {
		t.Fatal(err)
	}
	plain, err := ResolveVersionMount(cfg, external)
	if err != nil || plain.Library || plain.Members[0].Path != external {
		t.Fatalf("explicit target = %+v, %v", plain, err)
	}
}

func publishResolveVersion(t *testing.T, repo Repo, body string, parent ImageMeta, typ string) ImageMeta {
	t.Helper()
	node := versionNode(body)
	node.Format = Format{Type: typ, Compression: CompressionZstd}
	stage := writeSource(t, t.TempDir(), []byte(body))
	if parent.ID == "" {
		got, err := PublishFullVersion(linkImagesRoot(repo), repo.Name(), stage, node, false)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	node.Kind, node.Parent = KindIncremental, parent.ID
	got, err := PublishIncrementalVersion(linkImagesRoot(repo), repo.Name(), stage, node, parent, false)
	if err != nil {
		t.Fatal(err)
	}
	return got
}
