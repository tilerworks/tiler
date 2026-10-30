package workspace

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPruneProgressNilReceiverStaysQuiet(_ *testing.T) {
	var prog *pruneProgress
	prog.start()
	prog.setTotal(4)
	prog.advance(1)
	prog.advance(3)
	prog.succeed()
	prog.abandon()
}

func TestPruneProgressLiveFollowsCharacterDevice(t *testing.T) {
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = null.Close() })
	if !pruneProgressLive(null) {
		t.Fatal("DevNull is a character device")
	}
	file, err := os.CreateTemp(t.TempDir(), "progress")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	if pruneProgressLive(file) || pruneProgressLive(&bytes.Buffer{}) {
		t.Fatal("a regular file and a buffer are logs, not terminals")
	}
}

func TestPruneProgressNonTerminalOmits100AndSeparateLines(t *testing.T) {
	var buf bytes.Buffer
	prog := newPruneProgress(&buf)
	prog.setTotal(100)
	prog.start()
	prog.advance(1)
	prog.advance(1)
	prog.advance(38)
	prog.advance(60)
	prog.succeed()
	want := "pruning upper ...\n" +
		"pruning upper ... 1%\n" +
		"pruning upper ... 2%\n" +
		"pruning upper ... 40%\n" +
		"pruning upper ... done\n"
	if buf.String() != want {
		t.Fatalf("progress =\n%s", buf.String())
	}
	if strings.Contains(buf.String(), "100%") {
		t.Fatalf("progress printed 100%%:\n%s", buf.String())
	}
}

func TestPruneProgressTerminalRewritesOneLine(t *testing.T) {
	prev := pruneProgressLive
	pruneProgressLive = func(io.Writer) bool { return true }
	t.Cleanup(func() { pruneProgressLive = prev })

	var buf bytes.Buffer
	prog := newPruneProgress(&buf)
	prog.setTotal(100)
	prog.start()
	prog.advance(40)
	prog.advance(60)
	prog.succeed()
	want := "pruning upper ...\rpruning upper ... 40%\rpruning upper ... done\n"
	if buf.String() != want {
		t.Fatalf("progress = %q", buf.String())
	}
}

func TestPruneProgressAbandonOmitsDone(t *testing.T) {
	var buf bytes.Buffer
	prog := newPruneProgress(&buf)
	prog.setTotal(10)
	prog.start()
	prog.advance(4)
	prog.abandon()
	if buf.String() != "pruning upper ...\npruning upper ... 40%\n" {
		t.Fatalf("progress = %q", buf.String())
	}

	prev := pruneProgressLive
	pruneProgressLive = func(io.Writer) bool { return true }
	t.Cleanup(func() { pruneProgressLive = prev })
	buf.Reset()
	live := newPruneProgress(&buf)
	live.setTotal(10)
	live.start()
	live.advance(4)
	live.abandon()
	if buf.String() != "pruning upper ...\rpruning upper ... 40%\n" {
		t.Fatalf("live progress = %q", buf.String())
	}
	if strings.Contains(buf.String(), "done") {
		t.Fatalf("abandon printed done: %q", buf.String())
	}
}

func TestPruneProgressPercentRisesThrough99(t *testing.T) {
	upper, lower := fixtureRoots(t)
	anchorLower(t, lower)
	const entries = 101
	for i := 0; i < entries; i++ {
		writeFixtureFile(t, filepath.Join(upper, fmt.Sprintf("f%03d", i)), "x", 0o644)
	}
	var buf bytes.Buffer
	report, err := pruneWithProgress(upper, lower, plainHooks(), newPruneProgress(&buf))
	if err != nil {
		t.Fatal(err)
	}
	if report.Kept != entries {
		t.Fatalf("kept = %d, want %d", report.Kept, entries)
	}
	var want strings.Builder
	for pct := 1; pct <= 99; pct++ {
		fmt.Fprintf(&want, "pruning upper ... %d%%\n", pct)
	}
	if buf.String() != want.String() {
		t.Fatalf("progress lines = %d, want 99", bytes.Count(buf.Bytes(), []byte{'\n'}))
	}
	if strings.Contains(buf.String(), "100%") {
		t.Fatal("progress printed 100%")
	}
}

func TestPruneProgressRedirectCountsUnvisitedInOneStep(t *testing.T) {
	upper, lower := fixtureRoots(t)
	anchorLower(t, lower)
	writeFixtureFile(t, filepath.Join(upper, "a-file"), "keep", 0o644)
	dir := filepath.Join(upper, "z-dir")
	makeFixtureDir(t, dir)
	const children = 8
	for i := 0; i < children; i++ {
		writeFixtureFile(t, filepath.Join(dir, fmt.Sprintf("c%02d", i)), "c", 0o644)
	}
	seam := newFakePlatform()
	seam.mark(dir, xattrRedirect)
	var buf bytes.Buffer
	report, err := pruneWithProgress(upper, lower, seam.hooks(), newPruneProgress(&buf))
	if err != nil {
		t.Fatal(err)
	}
	if report.Kept != 1 || report.SkippedRedirect != 1 {
		t.Fatalf("report = %+v", report)
	}
	assertExists(t, filepath.Join(dir, "c00"))
	// Ten entries: the file, the redirect directory, and eight
	// unvisited children. One step for the subtree yields either
	// 10% (file first) or 90% (directory first), never 100% and
	// never a line per child.
	got := buf.String()
	if got != "pruning upper ... 10%\n" && got != "pruning upper ... 90%\n" {
		t.Fatalf("progress = %q", got)
	}
	if strings.Contains(got, "100%") {
		t.Fatalf("progress printed 100%%: %q", got)
	}
}

// progressEvents records prune progress lines beside mount calls.
type progressEvents struct {
	bytes.Buffer
	events *[]string
}

func (p *progressEvents) Write(b []byte) (int, error) {
	text := string(b)
	switch {
	case strings.Contains(text, "done"):
		*p.events = append(*p.events, "done")
	case strings.Contains(text, "%"):
		*p.events = append(*p.events, "percent")
	default:
		*p.events = append(*p.events, "start")
	}
	return p.Buffer.Write(b)
}

func TestMountPruneProgressOrdersStartLowercheckPruneDoneAndMerge(t *testing.T) {
	req := pruneRequest(t)
	var events []string
	req.Progress = &progressEvents{events: &events}
	k := newFakeKernel()
	var lowerOpts, mergedOpts string
	run := func(name string, args ...string) error {
		if name == "fuse-overlayfs" {
			opts := args[1]
			if strings.Contains(opts, "upperdir=") {
				events = append(events, "merged")
				mergedOpts = opts
			} else {
				events = append(events, "lowercheck")
				lowerOpts = opts
			}
		}
		return k.run(name, args...)
	}
	seam := k.seam()
	seam.prune = func(upperDir, lowerView string) (PruneReport, error) {
		events = append(events, "prune")
		if upperDir != filepath.Join(req.RuntimeDir, "upper") {
			t.Errorf("upper = %q", upperDir)
		}
		if lowerView != filepath.Join(req.RuntimeDir, "lowercheck") {
			t.Errorf("lower view = %q", lowerView)
		}
		return PruneReport{Kept: 1}, nil
	}
	w, err := mountTest(t, req, run, seam)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"start", "lowercheck", "prune", "done", "merged"}
	if strings.Join(events, ",") != strings.Join(want, ",") {
		t.Fatalf("events = %v, want %v", events, want)
	}
	if strings.Contains(lowerOpts, "upperdir=") || strings.Contains(lowerOpts, "workdir=") {
		t.Fatalf("lowercheck is not read-only: %q", lowerOpts)
	}
	if !strings.Contains(mergedOpts, "upperdir="+w.UpperDir) {
		t.Fatalf("merged view missing upperdir: %q", mergedOpts)
	}
	if req.Progress.(*progressEvents).String() != "pruning upper ...\npruning upper ... done\n" {
		t.Fatalf("progress = %q", req.Progress.(*progressEvents).String())
	}
	if w.PruneFailed {
		t.Fatal("a finished window reported an incomplete prune")
	}
}

func TestMountPruneProgressFailureOmitsDone(t *testing.T) {
	req := pruneRequest(t)
	var buf bytes.Buffer
	req.Progress = &buf
	warns := captureWarnings(t)
	k := newFakeKernel()
	seam := k.seam()
	seam.prune = func(string, string) (PruneReport, error) {
		return PruneReport{}, errors.New("prune: forced failure")
	}
	w, err := mountTest(t, req, k.run, seam)
	if err != nil {
		t.Fatalf("prune failure blocked the mount: %v", err)
	}
	if buf.String() != "pruning upper ...\n" {
		t.Fatalf("progress = %q, want the start line and no done", buf.String())
	}
	if strings.Contains(buf.String(), "done") || strings.Contains(buf.String(), "100%") {
		t.Fatalf("failed window wrote a completion mark: %q", buf.String())
	}
	if len(*warns) != 1 || !strings.Contains((*warns)[0], "forced failure") {
		t.Fatalf("warnings = %v", *warns)
	}
	if !w.PruneFailed {
		t.Fatal("a failed window left PruneFailed clear")
	}
}

func TestMountPruneProgressNilStaysSilentOnFailure(t *testing.T) {
	req := pruneRequest(t)
	if req.Progress != nil {
		t.Fatal("the fixture set a progress writer")
	}
	warns := captureWarnings(t)
	k := newFakeKernel()
	seam := k.seam()
	seam.prune = func(string, string) (PruneReport, error) {
		return PruneReport{}, errors.New("prune: forced failure")
	}
	w, err := mountTest(t, req, k.run, seam)
	if err != nil {
		t.Fatalf("prune failure blocked the mount: %v", err)
	}
	if !w.PruneFailed {
		t.Fatal("a failed window left PruneFailed clear")
	}
	if len(*warns) != 1 || !strings.Contains((*warns)[0], "forced failure") {
		t.Fatalf("warnings = %v, want the prune warning", *warns)
	}
	if strings.Contains((*warns)[0], "pruning upper") || strings.HasSuffix((*warns)[0], "done") {
		t.Fatalf("nil progress wrote a prune line: %q", (*warns)[0])
	}
}
