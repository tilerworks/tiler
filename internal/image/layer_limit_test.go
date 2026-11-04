package image

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jiangxin/tiler/internal/config"
)

func TestIncrementalBaseUsesConfiguredDepthAndSelectedBranch(t *testing.T) {
	for _, limit := range []int{1, 8, config.DefaultMaxLayers, 64} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			manifest := deepManifest(limit - 1)
			if err := checkIncrBase(manifest, "", limit); err == nil ||
				!strings.Contains(err.Error(), fmt.Sprintf("max_layers is %d", limit)) {
				t.Fatalf("accepted increment beyond limit: %v", err)
			}
			if limit > 1 {
				parent := manifest.Images[len(manifest.Images)-2].ID
				if err := checkIncrBase(manifest, parent, limit); err != nil {
					t.Fatalf("branch reaching the limit rejected: %v", err)
				}
			}
			if err := checkIncrBase(manifest, "", limit+1); err != nil {
				t.Fatalf("raised limit ignored: %v", err)
			}
		})
	}
}

func TestVersionIncrementalParentEnforcesConfiguredDepth(t *testing.T) {
	cfg := buildConfig(t)
	manifest := deepManifest(config.DefaultMaxLayers - 1)
	for i := range manifest.Images {
		manifest.Images[i].Format = manifest.Format
	}
	repo, err := OpenRepo(cfg.ImagesDir, manifest.Name)
	if err != nil {
		t.Fatal(err)
	}
	storeVersionIndexFixture(t, repo, VersionManifest{
		Version: VersionManifestVersion, Name: manifest.Name, Images: manifest.Images,
	})
	req := IncrRequest{Config: cfg, Name: repo.Name(), BaseTip: manifest.Head}
	if _, err := incrVersionParent(repo, req); err == nil || !strings.Contains(err.Error(), "max_layers is 32") {
		t.Fatalf("accepted increment beyond default limit: %v", err)
	}
	req.Config.MaxLayers = 64
	if parent, err := incrVersionParent(repo, req); err != nil || parent.ID != manifest.Head {
		t.Fatalf("raised limit rejected: %s, %v", parent.ID, err)
	}
	req.Config.MaxLayers = 2
	req.BaseTip = manifest.Images[0].ID
	if _, err := incrVersionParent(repo, req); err != nil {
		t.Fatalf("short historical branch rejected: %v", err)
	}
}
