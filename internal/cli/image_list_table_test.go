package cli

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/image"
)

func TestImageListRelativeTimeUsesCalendarMonths(t *testing.T) {
	previous := time.Local
	time.Local = time.FixedZone("test", 8*60*60)
	t.Cleanup(func() { time.Local = previous })
	now := time.Date(2026, 6, 30, 12, 0, 0, 0, time.Local)
	cases := []struct{ at, want string }{
		{"2026-01-02T03:04:59+08:00", "01-02 03:04"},
		{"2025-12-30T12:00:00+08:00", "6mo"},
		{"2025-06-30T12:00:00+08:00", "1y"},
		{"2024-12-30T12:00:00+08:00", "1y6mo"},
		{"2025-12-30T12:00:01+08:00", "5mo"},
		{"2027-12-30T12:00:00+08:00", "in 1y6mo"},
		{"2025-12-31T18:00:00Z", "01-01 02:00"},
	}
	for _, tc := range cases {
		at, err := time.Parse(time.RFC3339, tc.at)
		if err != nil {
			t.Fatal(err)
		}
		if got := formatImageListTime(at, now); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.at, got, tc.want)
		}
	}
	boundary := time.Date(2026, 1, 1, 0, 0, 0, 0, time.Local)
	if got := formatImageListTime(boundary.Add(-time.Minute), boundary); got != "<1mo" {
		t.Fatalf("year boundary = %q", got)
	}
	if got := formatImageListTime(time.Time{}, now); got != "-" {
		t.Fatalf("missing = %q", got)
	}
}

func TestImageListWrapsDescriptionsWithinEightyDisplayColumns(t *testing.T) {
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	for _, description := range []string{
		strings.Repeat("longword", 30),
		strings.Repeat("two words ", 30),
		strings.Repeat("中文描述", 30),
		strings.Repeat("👩🏽‍💻🇨🇳e\u0301 ", 30),
	} {
		node := versionNode("a", image.KindFull, "", at)
		node.Description = description
		view := versionImageView{Name: strings.Repeat("镜像📦", 40), Latest: &node, Images: []image.ImageMeta{node}}
		var out bytes.Buffer
		if err := writeVersionImageTableAt(&out, []versionImageView{view}, at, 80); err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
		if len(lines) != 4 || !strings.HasSuffix(lines[3], "…") {
			t.Fatalf("expected exactly two description lines: %s", &out)
		}
		column := displayWidth(lines[1][:strings.Index(lines[1], "DESCRIPTION")])
		if !strings.HasPrefix(lines[3], strings.Repeat(" ", column)) || strings.HasPrefix(lines[3], strings.Repeat(" ", column+1)) {
			t.Fatalf("description continuation misaligned: %q", lines[3])
		}
		for _, line := range lines {
			if displayWidth(line) > 80 || strings.ContainsRune(line, '\t') {
				t.Fatalf("line exceeds budget: %q", line)
			}
		}
	}
}

func TestImageListBoundsDeepTreesWithoutLosingIDs(t *testing.T) {
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	var nodes []image.ImageMeta
	parent := ""
	for i := range 40 {
		node := versionNode("a", image.KindIncremental, parent, at)
		node.ID = fmt.Sprintf("%064x", i+1)
		node.Size = 1<<63 - 1
		if parent == "" {
			node.Kind = image.KindFull
		}
		nodes = append(nodes, node)
		parent = node.ID
	}
	view := versionImageView{Name: "deep", Latest: &nodes[len(nodes)-1], Images: nodes}
	var out bytes.Buffer
	if err := writeVersionImageTableAt(&out, []versionImageView{view}, at, 80); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "… ") || !strings.Contains(out.String(), listID(parent)+" ★") {
		t.Fatalf("missing elision or latest mark: %s", &out)
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if displayWidth(line) > 80 {
			t.Fatalf("deep line too wide: %q", line)
		}
	}
}

func TestDisplayCellsPreserveCommonEmojiAndCombiningText(t *testing.T) {
	for text, want := range map[string]int{
		"abc": 3, "中文": 4, "📦": 2, "★": 1, "↪️": 2,
		"👩🏽‍💻": 2, "🇨🇳": 2, "e\u0301": 1, "⚡": 2, "✅": 2,
	} {
		if got := displayWidth(text); got != want {
			t.Errorf("%q width = %d, want %d", text, got, want)
		}
		if got := truncateDisplay(text+text+text, want+1); got != text+"…" {
			t.Errorf("truncated cluster: %q", got)
		}
	}
}
