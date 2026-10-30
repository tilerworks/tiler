package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompactStatusPathChoosesShorterCanonicalSpelling(t *testing.T) {
	for _, tc := range []struct{ path, cwd, want string }{
		{"", "/workspace", ""},
		{"/workspace/app", "/workspace", "app"},
		{"app", "/workspace", "app"},
		{"/workspace", "/workspace", "."},
		{"/a", "/workspace/deep", "/a"},
		{"/a", "/b", "/a"},
		{"/a/b", "/a/x", "/a/b"},
		{"/", "/workspace", "/"},
		{"/workspace/a\nb\x1b", "/workspace", `a\u000ab\u001b`},
	} {
		if got := compactStatusPath(tc.path, tc.cwd); got != tc.want {
			t.Errorf("compactStatusPath(%q, %q) = %q, want %q", tc.path, tc.cwd, got, tc.want)
		}
	}
	root := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	if got := compactStatusPath(filepath.Join(root, "gone", "app"), alias); got != filepath.Join("gone", "app") {
		t.Fatalf("canonical cwd = %q", got)
	}
	if got := compactStatusPath(filepath.Join(alias, "gone", "app"), root); got != filepath.Join("gone", "app") {
		t.Fatalf("canonical target = %q", got)
	}
}

func TestCompactStatusPathBoundsDisplayWidthAndKeepsTail(t *testing.T) {
	for _, size := range []int{62, 63, 64, 100} {
		base := strings.Repeat("x", size)
		got := compactStatusPath("/workspace/"+base, "/workspace")
		want := base
		if size > 63 {
			want = "…/" + strings.Repeat("x", 61)
		}
		if got != want || displayWidth(got) > 63 {
			t.Errorf("size %d: %q, want %q", size, got, want)
		}
	}
	path := "/" + strings.Repeat("ancestor/", 20) + "middle/parent/file"
	got := compactStatusPath(path, "/unrelated")
	if !strings.HasPrefix(got, "…/") || !strings.HasSuffix(got, "middle/parent/file") || displayWidth(got) > 63 {
		t.Fatalf("component suffix = %q", got)
	}
	// One more whole component must exceed the available suffix width.
	if displayWidth("ancestor/"+strings.TrimPrefix(got, "…/")) <= 61 {
		t.Fatalf("did not retain the maximum number of components: %q", got)
	}
	for _, cluster := range []string{"界", "e\u0301", "👩‍💻", "🇨🇳"} {
		base := strings.Repeat(cluster, 100)
		got := compactStatusPath("/workspace/"+base, "/workspace")
		suffix := strings.TrimPrefix(got, "…/")
		if !strings.HasPrefix(got, "…/") || displayWidth(got) > 63 || !strings.HasSuffix(base, suffix) {
			t.Errorf("cluster %q: %q", cluster, got)
		}
		for _, cell := range displayCells(suffix) {
			if cell.text != cluster {
				t.Errorf("split display cell %q in %q", cell.text, got)
			}
		}
	}
}
