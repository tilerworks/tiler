package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/image"
)

func TestCommandGroupsDefaultToListWithTextHint(t *testing.T) {
	home, _ := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	node := versionNode("a", image.KindFull, "", time.Now())
	node.Description = strings.Repeat("description ", 40)
	writeVersionForest(t, imagesDir, "alice/app", node)
	if _, stderr, _, err := runCmd(t, append(append([]string{}, lib...), "session", "new", "dev")...); err != nil {
		t.Fatalf("session fixture: %v\n%s", err, stderr)
	}
	for _, tc := range []struct {
		group string
		flags []string
	}{
		{"images", nil},
		{"image", []string{"-w", "80"}},
		{"session", nil},
		{"sessions", nil},
	} {
		t.Run(tc.group, func(t *testing.T) {
			listArgs := append(append([]string{}, lib...), tc.group, "list")
			listed, stderr, _, err := runCmd(t, append(listArgs, tc.flags...)...)
			if err != nil || stderr != "" || strings.Contains(listed, "💡") {
				t.Fatalf("explicit list: %v, %q, %q", err, listed, stderr)
			}
			groupArgs := append(append([]string{}, lib...), tc.group)
			out, stderr, _, err := runCmd(t, append(groupArgs, tc.flags...)...)
			want := "💡 Use 'ls' to hide this hint; use '-h' to see more subcommands.\n" + listed
			if err != nil || stderr != "" || out != want {
				t.Fatalf("group: %v, %q, %q; want %q", err, out, stderr, want)
			}
		})
	}
}

func TestDefaultListJSONAndHelpKeepTheirOutputContracts(t *testing.T) {
	home, _ := withWorkspace(t)
	_, lib := libFlags(home)
	if _, stderr, _, err := runCmd(t, append(append([]string{}, lib...), "session", "new", "dev")...); err != nil {
		t.Fatalf("session fixture: %v\n%s", err, stderr)
	}
	for _, group := range []string{"images", "image", "session", "sessions"} {
		args := append(append([]string{}, lib...), group)
		out, stderr, _, err := runCmd(t, append(args, "--json")...)
		if err != nil || stderr != "" || strings.Contains(out, "💡") {
			t.Fatalf("%s JSON: %v, %q, %q", group, err, out, stderr)
		}
		listed, _, _, err := runCmd(t, append(args, "list", "--json")...)
		if err != nil || listed != out {
			t.Fatalf("%s JSON differs from list: %v, %q, %q", group, err, out, listed)
		}
		help, stderr, _, err := runCmd(t, append(args, "-h")...)
		if err != nil || !strings.Contains(help, "Usage:") || !strings.Contains(help, "list") || strings.Contains(help, "💡") {
			t.Fatalf("%s help: %v, %q, %q", group, err, help, stderr)
		}
		out, _, _, err = runCmd(t, append(args, "not-a-subcommand")...)
		if ExitCode(err) != 2 || strings.Contains(out, "💡") {
			t.Fatalf("%s invalid command: %v, %q", group, err, out)
		}
	}
}
