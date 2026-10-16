package gitscene

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type gitCall struct {
	dir  string
	args []string
}

type fakeGit struct {
	calls   []gitCall
	respond func(args []string) (string, error)
}

func (f *fakeGit) run(dir, name string, args ...string) ([]byte, error) {
	if name != "git" {
		return nil, fmt.Errorf("unexpected tool %s", name)
	}
	f.calls = append(f.calls, gitCall{dir: dir, args: args})
	if f.respond != nil {
		out, err := f.respond(args)
		return []byte(out), err
	}
	return nil, nil
}

func (f *fakeGit) has(want ...string) bool {
	return slices.ContainsFunc(f.calls, func(c gitCall) bool {
		return slices.Equal(c.args, want)
	})
}

func (f *fakeGit) args() [][]string {
	out := make([][]string, len(f.calls))
	for i, c := range f.calls {
		out[i] = c.args
	}
	return out
}

// worktreeResponse answers like a normal work tree with an origin
// remote and a set of local config keys.
func worktreeResponse(args []string) (string, error) {
	switch {
	case slices.Equal(args, []string{"rev-parse", "--is-inside-work-tree"}):
		return "true\n", nil
	case slices.Equal(args, []string{"remote"}):
		return "origin\nupstream\n", nil
	case slices.Equal(args, []string{"config", "--local", "--list", "--name-only"}):
		return strings.Join([]string{
			"core.repositoryformatversion",
			"user.name",
			"credential.helper",
			"url.git@github.com:.insteadOf",
			"remote.origin.token",
		}, "\n") + "\n", nil
	}
	return "", nil
}

func workTreeFixture(t *testing.T) (repo, gitDir string) {
	t.Helper()
	repo = t.TempDir()
	gitDir = filepath.Join(repo, ".git")
	for _, dir := range []string{filepath.Join(gitDir, "logs", "refs"), filepath.Join(gitDir, "hooks")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []string{
		filepath.Join(gitDir, "logs", "HEAD"),
		filepath.Join(gitDir, "FETCH_HEAD"),
		filepath.Join(gitDir, "hooks", "pre-commit"),
		filepath.Join(gitDir, "hooks", "pre-commit.sample"),
	} {
		if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return repo, gitDir
}

func TestPrepareWorkTreeCleansAndIndexes(t *testing.T) {
	repo, gitDir := workTreeFixture(t)
	fake := &fakeGit{respond: worktreeResponse}
	if err := prepare(repo, fake.run); err != nil {
		t.Fatal(err)
	}
	for _, call := range fake.calls {
		if call.dir != repo {
			t.Fatalf("call %v runs outside the repository: %s", call.args, call.dir)
		}
	}
	for _, want := range [][]string{
		{"rev-parse", "--is-inside-work-tree"},
		{"remote"},
		{"remote", "remove", "origin"},
		{"config", "--local", "--list", "--name-only"},
		{"config", "--local", "--unset-all", "credential.helper"},
		{"config", "--local", "--unset-all", "url.git@github.com:.insteadOf"},
		{"config", "--local", "--unset-all", "remote.origin.token"},
		{"commit-graph", "write", "--reachable"},
		{"multi-pack-index", "write"},
	} {
		if !fake.has(want...) {
			t.Errorf("missing call %q in %v", want, fake.args())
		}
	}
	if fake.has("remote", "remove", "upstream") {
		t.Error("removed a remote other than origin")
	}
	if fake.has("config", "--local", "--unset-all", "user.name") {
		t.Error("unset an unrelated config key")
	}
	assertGone(t, filepath.Join(gitDir, "logs"))
	assertGone(t, filepath.Join(gitDir, "FETCH_HEAD"))
	assertGone(t, filepath.Join(gitDir, "hooks", "pre-commit"))
	if _, err := os.Stat(filepath.Join(gitDir, "hooks", "pre-commit.sample")); err != nil {
		t.Errorf("sample hook deleted: %v", err)
	}
	last := fake.args()[len(fake.calls)-2:]
	if !slices.Equal(last[0], []string{"commit-graph", "write", "--reachable"}) ||
		!slices.Equal(last[1], []string{"multi-pack-index", "write"}) {
		t.Errorf("indexes are not written last: %v", last)
	}
}

func TestPrepareBareRepositoryUsesRepoDir(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "objects", "pack"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "hooks", "post-update"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "FETCH_HEAD"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := &fakeGit{respond: func(args []string) (string, error) {
		switch {
		case slices.Equal(args, []string{"rev-parse", "--is-inside-work-tree"}):
			return "false\n", nil
		case slices.Equal(args, []string{"rev-parse", "--is-bare-repository"}):
			return "true\n", nil
		}
		return "", nil
	}}
	if err := prepare(repo, fake.run); err != nil {
		t.Fatal(err)
	}
	if !fake.has("rev-parse", "--is-bare-repository") {
		t.Error("bare detection did not run")
	}
	assertGone(t, filepath.Join(repo, "hooks", "post-update"))
	assertGone(t, filepath.Join(repo, "FETCH_HEAD"))
	if !fake.has("commit-graph", "write", "--reachable") {
		t.Error("indexes not written for a bare repository")
	}
}

func TestPrepareFollowsGitDirFile(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "work")
	gitDir := filepath.Join(root, "admin")
	for _, dir := range []string{repo, filepath.Join(gitDir, "hooks")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, ".git"), []byte("gitdir: ../admin\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "FETCH_HEAD"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "hooks", "pre-push"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := &fakeGit{respond: worktreeResponse}
	if err := prepare(repo, fake.run); err != nil {
		t.Fatal(err)
	}
	assertGone(t, filepath.Join(gitDir, "FETCH_HEAD"))
	assertGone(t, filepath.Join(gitDir, "hooks", "pre-push"))
}

func TestPrepareRejectsMalformedGitDirFile(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, ".git"), []byte("nonsense\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := &fakeGit{respond: worktreeResponse}
	err := prepare(repo, fake.run)
	if err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("err = %v", err)
	}
}

func TestPrepareRejectsNonRepository(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	fake := &fakeGit{respond: func([]string) (string, error) {
		return "false\n", nil
	}}
	err := prepare(repo, fake.run)
	if err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("err = %v", err)
	}
	if fake.has("commit-graph", "write", "--reachable") {
		t.Error("indexes written for a non-repository")
	}
}

func TestPreparePropagatesRevParseFailure(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	fake := &fakeGit{respond: func([]string) (string, error) {
		return "", errors.New("git: fatal: not a git repository")
	}}
	if err := prepare(repo, fake.run); err == nil {
		t.Fatal("expected rev-parse failure")
	}
}

func TestPrepareGitFailureStopsBeforeIndexes(t *testing.T) {
	repo, gitDir := workTreeFixture(t)
	fake := &fakeGit{respond: func(args []string) (string, error) {
		if args[0] == "commit-graph" {
			return "", errors.New("git: fatal: commit-graph failure")
		}
		return worktreeResponse(args)
	}}
	err := prepare(repo, fake.run)
	if err == nil || !strings.Contains(err.Error(), "commit-graph failure") {
		t.Fatalf("err = %v", err)
	}
	if fake.has("multi-pack-index", "write") {
		t.Error("multi-pack-index ran after a failed commit-graph")
	}
	// The cleanups before the failure still happened.
	assertGone(t, filepath.Join(gitDir, "logs"))
}

func TestPrepareRemoteRemoveFailureAborts(t *testing.T) {
	repo, _ := workTreeFixture(t)
	fake := &fakeGit{respond: func(args []string) (string, error) {
		if slices.Equal(args, []string{"remote", "remove", "origin"}) {
			return "", errors.New("git: error: could not remove remote")
		}
		return worktreeResponse(args)
	}}
	if err := prepare(repo, fake.run); err == nil {
		t.Fatal("expected remote remove failure")
	}
	if fake.has("commit-graph", "write", "--reachable") {
		t.Error("indexes written after a failed remote remove")
	}
}

func TestIsGitDetectsLayouts(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "work")
	bare := filepath.Join(root, "bare")
	plain := filepath.Join(root, "plain")
	for _, dir := range []string{
		filepath.Join(work, ".git"),
		filepath.Join(bare, "objects"),
		plain,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(bare, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !IsGit(work) {
		t.Error("work tree with .git dir not detected")
	}
	if !IsGit(bare) {
		t.Error("bare repository not detected")
	}
	if IsGit(plain) {
		t.Error("plain directory detected as git")
	}
	linked := filepath.Join(root, "linked")
	if err := os.MkdirAll(linked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(linked, ".git"), []byte("gitdir: ../work/.git\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !IsGit(linked) {
		t.Error("work tree with .git file not detected")
	}
}

func TestPrepareRealRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, "gitconfig"))
	t.Setenv("GIT_CONFIG_SYSTEM", filepath.Join(home, "gitsystem"))
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return string(out)
	}
	git("init", "-b", "main")
	for i, body := range []string{"one", "two"} {
		file := filepath.Join(repo, fmt.Sprintf("file-%d.txt", i))
		if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		git("add", ".")
		git("-c", "user.name=test", "-c", "user.email=test@example.com",
			"commit", "-m", fmt.Sprintf("commit %d", i))
	}
	git("repack", "-d")
	git("remote", "add", "origin", "https://example.com/alice/app.git")
	git("config", "--local", "credential.helper", "store")
	git("config", "--local", "url.git@example.com:.insteadOf", "https://example.com/")
	gitDir := filepath.Join(repo, ".git")
	if err := os.WriteFile(filepath.Join(gitDir, "hooks", "pre-commit"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "hooks", "pre-commit.sample"), []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "FETCH_HEAD"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Prepare(repo); err != nil {
		t.Fatal(err)
	}

	if out := git("remote"); strings.TrimSpace(out) != "" {
		t.Errorf("remotes left behind: %q", out)
	}
	list := git("config", "--local", "--list", "--name-only")
	for _, line := range strings.Split(list, "\n") {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "credential") || strings.Contains(lower, "insteadof") ||
			strings.Contains(lower, "token") {
			t.Errorf("leaky config key left behind: %s", line)
		}
	}
	assertGone(t, filepath.Join(gitDir, "hooks", "pre-commit"))
	assertGone(t, filepath.Join(gitDir, "FETCH_HEAD"))
	assertGone(t, filepath.Join(gitDir, "logs"))
	if _, err := os.Stat(filepath.Join(gitDir, "hooks", "pre-commit.sample")); err != nil {
		t.Errorf("sample hook deleted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(gitDir, "objects", "info", "commit-graph")); err != nil {
		t.Errorf("commit-graph missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(gitDir, "objects", "pack", "multi-pack-index")); err != nil {
		t.Errorf("multi-pack-index missing: %v", err)
	}
}

func assertGone(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%s still exists: %v", path, err)
	}
}
