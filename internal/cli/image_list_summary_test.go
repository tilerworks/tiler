package cli

import (
	"bytes"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/image"
)

func TestImageSummaryAggregatesBranchesAndUnknownActivity(t *testing.T) {
	now := goldenSessionTime
	base := versionNode("a", image.KindFull, "", now)
	child := versionNode("b", image.KindIncremental, base.ID, now)
	sibling := versionNode("c", image.KindIncremental, base.ID, now)
	base.Size, child.Size, sibling.Size = 1024, 2048, 4096
	view := versionImageView{Name: "alice/app", Latest: &child, Images: []image.ImageMeta{base, child, sibling}, Activity: map[string]imageListActivity{
		base.ID: {At: now.Add(-time.Hour)}, child.ID: {At: now.Add(-time.Minute)}, sibling.ID: {At: now},
	}}
	row, err := imageSummaryRow(view, now)
	want := []string{"alice/app", "3", formatSize(7168), formatImageListTime(now, now)}
	if err != nil || !reflect.DeepEqual(row, want) {
		t.Fatalf("row = %v, %v; want %v", row, err, want)
	}
	view.Activity[base.ID] = imageListActivity{Unknown: true}
	row, err = imageSummaryRow(view, now)
	if err != nil || row[3] != "unknown" {
		t.Fatalf("unknown: %v %v", row, err)
	}
	view.Activity = nil
	row, err = imageSummaryRow(view, now)
	if err != nil || row[3] != "-" {
		t.Fatalf("missing: %v %v", row, err)
	}
	view.Images[0].Size = math.MaxInt64
	if _, err := imageSummaryRow(view, now); err == nil {
		t.Fatal("overflow accepted")
	}
}

func TestImageSummaryWidthEmptyRepositoryAndWriterErrors(t *testing.T) {
	views := []versionImageView{{Name: strings.Repeat("镜像📦", 60)}, {Name: "empty/image"}}
	var out bytes.Buffer
	if err := writeImageSummaryAt(&out, views, goldenSessionTime, 80); err != nil {
		t.Fatal(err)
	}
	assertOutputWidth(t, out.String(), 80)
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 || !strings.Contains(lines[0], "VERSIONS  TOTAL SIZE  LAST ACTIVITY") || !strings.Contains(lines[1], "…") || strings.Join(strings.Fields(lines[2]), " ") != "empty/image 0 0 B -" {
		t.Fatalf("summary:\n%s", &out)
	}
	out.Reset()
	if err := writeImageSummaryAt(&out, nil, goldenSessionTime, 80); err != nil || out.String() != "No images found.\n" {
		t.Fatalf("empty: %s %v", &out, err)
	}
	failure := errors.New("broken pipe")
	if err := writeImageSummaryAt(failWriter{failure}, views, goldenSessionTime, 80); !errors.Is(err, failure) {
		t.Fatalf("writer: %v", err)
	}
}

func TestImagesSummaryVerboseAliasesAndJSON(t *testing.T) {
	home, _ := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	at := time.Now()
	node := versionNode("a", image.KindFull, "", at)
	writeVersionForest(t, imagesDir, "alice/app", node)
	var document string
	for _, entry := range [][]string{{"images", "ls"}, {"image", "list"}, {"images"}, {"image"}} {
		for _, flag := range []string{"", "-v", "--verbose", "--verbose=false"} {
			args := append(append([]string{}, lib...), entry...)
			if flag != "" {
				args = append(args, flag)
			}
			out, stderr, _, err := runCmd(t, args...)
			if err != nil {
				t.Fatalf("%v: %v %s", args, err, stderr)
			}
			verbose := flag == "-v" || flag == "--verbose"
			if strings.Contains(out, "DESCRIPTION") != verbose || strings.Contains(out, "TOTAL SIZE") == verbose {
				t.Fatalf("mode %v: %s", args, out)
			}
			if !verbose && !strings.Contains(out, "alice/app") {
				t.Fatalf("missing image: %s", out)
			}
			out, stderr, _, err = runCmd(t, append(args, "--json")...)
			if err != nil {
				t.Fatalf("JSON: %v %s", err, stderr)
			}
			if document != "" && document != out {
				t.Fatal("verbosity or alias changed JSON")
			}
			document = out
		}
	}
}
