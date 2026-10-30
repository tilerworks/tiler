package cli

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jiangxin/tiler/internal/image"
)

func TestImagesGCSelectedNamesPreviewAndRemoval(t *testing.T) {
	for _, tc := range []struct {
		name     string
		patterns []string
		selected []string
	}{
		{"exact", []string{"alice/app"}, []string{"alice/app"}},
		{"nested glob", []string{"alice/*"}, []string{"alice/app", "alice/tools/app"}},
		{"multiple and overlap", []string{"alice/*", "alice/app", "bob/app"}, []string{"alice/app", "alice/tools/app", "bob/app"}},
		{"mixed unmatched", []string{"alice/app", "missing/*"}, []string{"alice/app"}},
		{"unmatched", []string{"missing/*"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, lib, base, nodes := imageGCFixture(t)
			imagesDir, _ := libFlags(home)
			repos := []image.Repo{base}
			for _, name := range []string{"alice/tools/app", "bob/app"} {
				writeVersionForest(t, imagesDir, name, nodes...)
				repo, err := image.OpenRepo(imagesDir, name)
				if err != nil {
					t.Fatal(err)
				}
				for _, node := range nodes {
					writeImmutablePayload(t, repo, node, []byte("x"))
					seedImageGCActivity(t, imagesDir, repo, node.ID, node.CreatedAt)
				}
				repos = append(repos, repo)
			}
			orphan := strings.Repeat("c", 64)
			for _, repo := range repos {
				dir, _ := repo.ObjectDirPath(orphan)
				writeTestFile(t, filepath.Join(dir, "payload"), "orphan")
			}
			withTerminalSeam(t, true)
			for _, flag := range []string{"-n", "--dry-run", ""} {
				args := append([]string{"--yes"}, tc.patterns...)
				if flag != "" {
					args = append(args, flag)
				}
				out, stderr, err := runCmdReader(t, failReader{errors.New("must not read")}, imageGCArgs(lib, args...)...)
				if err != nil || strings.Contains(stderr, "Type yes") {
					t.Fatalf("out=%q stderr=%q error=%v", out, stderr, err)
				}
				if slices.Contains(tc.patterns, "missing/*") && !strings.Contains(stderr, `No images match "missing/*".`) {
					t.Fatalf("missing unmatched diagnostic: %q", stderr)
				}
				for _, repo := range repos {
					selected := slices.Contains(tc.selected, repo.Name())
					for _, id := range []string{nodes[0].ID, nodes[1].ID, orphan} {
						if (strings.Count(out, "  "+repo.Name()+":"+id) == 1) != selected {
							t.Fatalf("incorrect candidate %s:%s in %q", repo.Name(), id, out)
						}
						dir, _ := repo.ObjectDirPath(id)
						_, err := os.Stat(dir)
						if (err == nil) != (flag != "" || !selected) || (err != nil && !os.IsNotExist(err)) {
							t.Fatalf("object %s:%s after %s: %v", repo.Name(), id, flag, err)
						}
					}
				}
			}
		})
	}
}

func TestImagesGCInvalidSelectorDoesNotInitializeActivity(t *testing.T) {
	home, lib, repo, nodes := imageGCFixture(t)
	log := filepath.Join(home, ".tiler", "logs", "images", "alice", "app", nodes[0].ID+".log")
	if err := os.Remove(log); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := runCmd(t, imageGCArgs(lib, "--yes", "--dry-run", "alice/*", "[")...)
	if ExitCode(err) != 2 {
		t.Fatalf("invalid pattern error=%v", err)
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatalf("invalid pattern initialized activity: %v", err)
	}
	assertImageGCObjects(t, repo, nodes, true)
}
