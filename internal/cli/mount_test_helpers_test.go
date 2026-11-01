package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/config"
	"github.com/jiangxin/tiler/internal/image"
	"github.com/jiangxin/tiler/internal/mountinfo"
	"github.com/jiangxin/tiler/internal/session"
	"github.com/jiangxin/tiler/internal/workspace"
)

type cacheCall struct {
	dir, src, want, out string
}

type mountFake struct {
	table      []mountinfo.Mount
	cacheCalls []cacheCall
	toolCalls  [][]string
	mountReqs  []workspace.Request
	umountReqs []workspace.UnmountRequest
	reused     bool
	// pruneFailed reports an unfinished prune window from the mount
	// seam. The real window's warning and omitted done line are
	// covered in the workspace tests.
	pruneFailed bool
	removed     int
	mountErr    error
	umountErr   error
	// mountHook runs inside one mount call, so a test can change the
	// filesystem between the request and the recorded outcome.
	mountHook func()

	assembleCalls      [][]string
	assembleBlockPaths [][]string
	assembleN          int
	assembleOut        string
	assembleErr        error
}

func (f *mountFake) cache(cacheDir, src, want string) (string, string, error) {
	out := filepath.Join(cacheDir, "cached-"+filepath.Base(src))
	f.cacheCalls = append(f.cacheCalls, cacheCall{cacheDir, src, want, out})
	return out, strings.Repeat("ab", 32), nil
}

func (f *mountFake) runTool(name string, args ...string) error {
	f.toolCalls = append(f.toolCalls, append([]string{name}, args...))
	return nil
}

func (f *mountFake) mount(req workspace.Request) (*workspace.Workspace, error) {
	f.mountReqs = append(f.mountReqs, req)
	if f.mountHook != nil {
		f.mountHook()
	}
	if f.mountErr != nil {
		return nil, f.mountErr
	}
	return &workspace.Workspace{
		MergedDir:   req.MergedDir,
		Reused:      f.reused,
		PruneFailed: f.pruneFailed,
	}, nil
}

func (f *mountFake) umount(req workspace.UnmountRequest) (int, error) {
	f.umountReqs = append(f.umountReqs, req)
	if f.umountErr != nil {
		return 0, f.umountErr
	}
	return f.removed, nil
}

func (f *mountFake) assemble(cacheDir string, blocks []image.BlockFile, wantSHA string, wantSize int64) (string, error) {
	f.assembleN++
	f.assembleCalls = append(f.assembleCalls, []string{cacheDir, wantSHA, fmt.Sprint(wantSize), fmt.Sprint(len(blocks))})
	paths := make([]string, len(blocks))
	for i, block := range blocks {
		paths[i] = block.Path
	}
	f.assembleBlockPaths = append(f.assembleBlockPaths, paths)
	if f.assembleErr != nil {
		return "", f.assembleErr
	}
	out := f.assembleOut
	if out == "" {
		out = filepath.Join(cacheDir, wantSHA+image.SquashExt)
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(out, []byte("hsqs-reassembled"), 0o644); err != nil {
		return "", err
	}
	return out, nil
}

func withMountSeams(t *testing.T, table []mountinfo.Mount) *mountFake {
	t.Helper()
	withSessionLockSeam(t)
	f := &mountFake{table: table}
	origRun, origCache, origTable := mountRun, ensureCache, loadMountTable
	origMount, origUmount, origAssemble := mountEngine, unmountEngine, assembleBlocks
	origConcat := concatAvailable
	t.Cleanup(func() {
		mountRun, ensureCache, loadMountTable = origRun, origCache, origTable
		mountEngine, unmountEngine, assembleBlocks = origMount, origUmount, origAssemble
		concatAvailable = origConcat
	})
	mountRun, ensureCache = f.runTool, f.cache
	loadMountTable = func() ([]mountinfo.Mount, error) { return f.table, nil }
	mountEngine, unmountEngine, assembleBlocks = f.mount, f.umount, f.assemble
	concatAvailable = func() bool { return false }
	installFoundTools(t)
	return f
}

func withSessionLockSeam(t *testing.T) { withSessionSeams(t, nil) }

func withSessionSeams(t *testing.T, tune func(*session.Engine)) {
	t.Helper()
	original := sessionEngine
	t.Cleanup(func() { sessionEngine = original })
	sessionEngine = func(cfg config.Config) *session.Engine {
		engine := original(cfg)
		engine.Versioned = false
		engine.Lock = func(string, time.Duration) (func(), error) { return func() {}, nil }
		engine.LoadDiskImages = nil
		if tune != nil {
			tune(engine)
		}
		return engine
	}
}

func healthySessions(engine *session.Engine) {
	engine.Health = func(string) bool { return true }
}

func buildLibrary(t *testing.T, home string, lib []string, imagesDir, name string) string {
	t.Helper()
	src := filepath.Join(home, "work", "src")
	writeTestFile(t, filepath.Join(src, "hello.txt"), "hello")
	withBuildSeams(t, nil)
	buildImage(t, lib, name, src)
	repo, err := image.OpenRepo(imagesDir, name)
	if err != nil {
		t.Fatal(err)
	}
	return repo.Dir
}
