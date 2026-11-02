package cli

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/config"
)

func TestSplitEditor(t *testing.T) {
	checkSplit(t, `'/tmp/editor with spaces' --wait`, []string{"/tmp/editor with spaces", "--wait"}, false)
	checkSplit(t, `'/tmp/editor`, nil, true)
}

func checkSplit(t *testing.T, value string, want []string, wantErr bool) {
	t.Helper()
	got, err := splitEditor(value)
	if (err != nil) != wantErr || strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("splitEditor(%q) = %q, %v; want %q, error %v", value, got, err, want, wantErr)
	}
}

func TestDescriptionEditorCommandOutcomes(t *testing.T) {
	oldTerminal, oldRun := inputTerminal, runDescriptionEditor
	t.Cleanup(func() { inputTerminal, runDescriptionEditor = oldTerminal, oldRun })
	for _, tc := range []struct{ name, edit, wantErr string }{
		{name: "editor exit", wantErr: "invalid argument"}, {name: "unchanged"},
		{name: "clear", edit: "\n"}, {name: "noninteractive", wantErr: "non-interactive"},
		{name: "concurrent deletion", edit: "changed", wantErr: "was removed"},
	} {
		dir := t.TempDir()
		node := versionNode("a", "full", "", time.Now().UTC())
		writeVersionForest(t, dir, "alice/app", node)
		inputTerminal = func(io.Reader) bool { return tc.name != "noninteractive" }
		t.Setenv("EDITOR", `"/tmp/editor with spaces" --wait`)
		runDescriptionEditor = func(_ context.Context, name string, args []string, _ io.Reader, _, _ io.Writer) error {
			info, err := os.Stat(args[1])
			if name != "/tmp/editor with spaces" || args[0] != "--wait" || err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("editor invocation = %q %q, %v, %v", name, args, info, err)
			}
			if tc.name == "editor exit" {
				return os.ErrInvalid
			}
			if tc.name == "concurrent deletion" {
				writeVersionForest(t, dir, "alice/app")
			}
			if tc.edit != "" || tc.name == "clear" {
				return os.WriteFile(args[1], []byte(tc.edit), 0o600)
			}
			return nil
		}
		cmd := newImagesDescribeCmd()
		cmd.SetContext(context.Background())
		remember(cmd, config.Config{ImagesDir: dir})
		cmd.SetArgs([]string{"alice/app:" + node.ID[:4]})
		err := cmd.Execute()
		if (err == nil) != (tc.wantErr == "") || err != nil && !strings.Contains(err.Error(), tc.wantErr) {
			t.Fatalf("error = %v, want %q", err, tc.wantErr)
		}
		if tc.name == "clear" {
			view, readErr := collectVersionShow(dir, "alice/app:"+node.ID[:4])
			if readErr != nil || view.Selected.Description != "" {
				t.Fatalf("description was not cleared: %q, %v", view.Selected.Description, readErr)
			}
		}
	}
}
