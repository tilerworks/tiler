package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/jiangxin/tiler/internal/image"
	"github.com/jiangxin/tiler/internal/session"
)

func TestBuildIncrementalUsesMountedVersionInsteadOfLatest(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	merged := filepath.Join(work, "merged")
	upper := filepath.Join(sessionsDir(home), "dev", "runtime", "m1", "upper")
	src := filepath.Join(work, "src")
	withBuildSeams(t, viewTable(imagesDir, "alice/app", merged, upper))
	writeTestFile(t, filepath.Join(src, "base.txt"), "base")
	base := buildVersionImage(t, lib, "alice/app", src)
	writeTestFile(t, filepath.Join(src, "base.txt"), "unrelated latest")
	latest := buildVersionImage(t, lib, "alice/app", src)
	writeSessionRecord(t, sessionsDir(home), "dev", pinnedAction("alice/app", merged, session.ModeRW, base, base))
	t.Chdir(work)
	for _, contents := range []string{"first layer", "changed upper"} {
		writeTestFile(t, filepath.Join(upper, "new.txt"), contents)
		out, stderr, _, err := runCmd(t, append(lib, "build", "--incremental", merged)...)
		if err != nil {
			t.Fatalf("build: %v\n%s", err, stderr)
		}
		id := strings.Fields(out)[2]
		repo, err := image.OpenRepo(imagesDir, "alice/app")
		if err != nil {
			t.Fatal(err)
		}
		manifest, err := image.LoadVersionManifest(repo)
		if err != nil {
			t.Fatal(err)
		}
		stack, err := manifest.SelectVersion("alice/app:" + id)
		if err != nil {
			t.Fatal(err)
		}
		node := stack[len(stack)-1]
		if node.Parent != base {
			t.Fatalf("parent = %s, want mounted %s (latest was %s)", node.Parent, base, latest)
		}
	}
}
