package image

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/jiangxin/tiler/internal/activity"
)

// testActivityNow is the injected clock of the activity batch tests.
var testActivityNow = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

// activityClock returns the fixed test clock.
func activityClock() time.Time { return testActivityNow }

// incrementalNode derives one incremental version from parent.
func incrementalNode(parent ImageMeta, text string) ImageMeta {
	node := versionNode(text)
	node.Kind, node.Parent = KindIncremental, parent.ID
	return node
}

// seedVersions commits metadata-only versions without the publication
// activity a real publish records, which is what a library upgraded
// from an older release looks like, and creates the object directory
// each payload and activity log lives in.
func seedVersions(t *testing.T, repo Repo, nodes ...ImageMeta) {
	t.Helper()
	manifest, previous, err := loadVersionMeta(repo)
	if err != nil {
		t.Fatalf("seed manifest: %v", err)
	}
	manifest.Images = append(manifest.Images, nodes...)
	body, err := EncodeVersionManifest(manifest)
	if err != nil {
		t.Fatalf("encode seeded manifest: %v", err)
	}
	if err := os.MkdirAll(repo.Dir, 0o755); err != nil {
		t.Fatalf("create repository directory: %v", err)
	}
	if err := replaceVersionMeta(repo, previous, body, hooks{}); err != nil {
		t.Fatalf("write seeded manifest: %v", err)
	}
	for _, node := range nodes {
		dir, err := repo.ObjectDirPath(node.ID)
		if err != nil {
			t.Fatalf("object dir of %s: %v", node.Description, err)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create object dir of %s: %v", node.Description, err)
		}
	}
}

// versionActivityLine is one decoded version activity record.
type versionActivityLine struct {
	Time          string `json:"time"`
	Event         string `json:"event"`
	Phase         string `json:"phase"`
	Image         string `json:"image"`
	Version       string `json:"version"`
	Session       string `json:"session"`
	InstanceID    string `json:"instance_id"`
	Seq           int    `json:"seq"`
	SourceVersion string `json:"source_version"`
	SourceEvent   string `json:"source_event"`
	Mountpoint    string `json:"mountpoint"`
	Error         string `json:"error"`
}

// activityLogPath locates the activity log of one version.
func activityLogPath(t *testing.T, repo Repo, id string) string {
	t.Helper()
	dir, err := repo.ObjectDirPath(id)
	if err != nil {
		t.Fatalf("object dir: %v", err)
	}
	return filepath.Join(dir, activity.LogName)
}

// readVersionActivity decodes one version log; a missing log reads as
// no records.
func readVersionActivity(t *testing.T, repo Repo, id string) []versionActivityLine {
	t.Helper()
	data, err := os.ReadFile(activityLogPath(t, repo, id))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("read activity log: %v", err)
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Fatalf("activity log of %s is not line terminated: %q", id, data)
	}
	var lines []versionActivityLine
	for _, text := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		var line versionActivityLine
		if err := json.Unmarshal([]byte(text), &line); err != nil {
			t.Fatalf("activity record %q: %v", text, err)
		}
		lines = append(lines, line)
	}
	return lines
}

// requireNoActivityLog proves one version never received a log.
func requireNoActivityLog(t *testing.T, repo Repo, id string) {
	t.Helper()
	if lines := readVersionActivity(t, repo, id); len(lines) != 0 {
		t.Fatalf("version %s recorded %+v, want no activity", id, lines)
	}
}

// TestVersionActivityUsesTheWallClockByDefault appends without an
// injected clock, which is what production callers do.
func TestVersionActivityUsesTheWallClockByDefault(t *testing.T) {
	repo := testRepo(t)
	base := versionNode("base")
	tip := incrementalNode(base, "tip")
	seedVersions(t, repo, base, tip)
	before := time.Now().UTC().Add(-time.Minute)
	ev := activity.Event{Event: activity.EventMount, Phase: activity.PhaseSucceeded}
	if err := AppendVersionActivityLocked(repo, tip.ID, ev, nil); err != nil {
		t.Fatalf("append: %v", err)
	}
	for _, node := range []ImageMeta{base, tip} {
		lines := readVersionActivity(t, repo, node.ID)
		stamp, err := time.Parse(time.RFC3339Nano, lines[0].Time)
		if err != nil {
			t.Fatalf("version %s time: %v", node.Description, err)
		}
		if stamp.Before(before) || stamp.After(time.Now().UTC().Add(time.Minute)) {
			t.Fatalf("version %s time = %v, want the wall clock", node.Description, stamp)
		}
	}
}

// TestVersionActivityRejectsAnInvalidEvent leaves every log alone when
// the event itself cannot be recorded.
func TestVersionActivityRejectsAnInvalidEvent(t *testing.T) {
	repo := testRepo(t)
	base := versionNode("base")
	tip := incrementalNode(base, "tip")
	seedVersions(t, repo, base, tip)
	for name, ev := range map[string]activity.Event{
		"empty event": {Phase: activity.PhaseStarted},
		"bad phase":   {Event: activity.EventMount, Phase: "halfway"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := AppendVersionActivityLocked(repo, tip.ID, ev, activityClock); !errors.Is(err, activity.ErrInvalidEvent) {
				t.Fatalf("append err = %v, want ErrInvalidEvent", err)
			}
			requireNoActivityLog(t, repo, base.ID)
			requireNoActivityLog(t, repo, tip.ID)
		})
	}
}

// TestEnsureVersionActivityInitializesAMissingChild keeps an existing
// parent log and adds the dependency initialization record to it.
func TestEnsureVersionActivityInitializesAMissingChild(t *testing.T) {
	repo := testRepo(t)
	base := versionNode("base")
	tip := incrementalNode(base, "tip")
	seedVersions(t, repo, base, tip)
	if err := AppendVersionActivityLocked(repo, base.ID,
		activity.Event{Event: activity.EventImageCreateFull, Phase: activity.PhaseSucceeded}, activityClock); err != nil {
		t.Fatalf("create the parent log: %v", err)
	}
	written, err := EnsureVersionActivityLocked(repo, tip.ID, activityClock)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if strings.Join(written, ",") != base.ID+","+tip.ID {
		t.Fatalf("written = %v, want root then source", written)
	}
	parent := readVersionActivity(t, repo, base.ID)
	if len(parent) != 2 {
		t.Fatalf("parent lines = %+v, want the create and the initialization", parent)
	}
	init := parent[1]
	if init.Event != activity.EventImageDependency || init.Phase != activity.PhaseInitialized {
		t.Fatalf("parent record = %+v, want a dependency initialization", init)
	}
	if init.Version != base.ID || init.SourceVersion != tip.ID ||
		init.SourceEvent != activity.EventInitialize {
		t.Fatalf("parent attribution = %+v", init)
	}
	source := readVersionActivity(t, repo, tip.ID)
	if len(source) != 1 || source[0].Event != activity.EventInitialize ||
		source[0].Phase != activity.PhaseInitialized || source[0].SourceVersion != "" {
		t.Fatalf("source record = %+v, want the initialization itself", source)
	}
}

// TestEnsureVersionActivityInitializesTheWholeChain builds every
// missing log from the dependency records.
func TestEnsureVersionActivityInitializesTheWholeChain(t *testing.T) {
	repo := testRepo(t)
	base := versionNode("base")
	mid := incrementalNode(base, "mid")
	tip := incrementalNode(mid, "tip")
	seedVersions(t, repo, base, mid, tip)
	written, err := EnsureVersionActivityLocked(repo, tip.ID, activityClock)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if len(written) != 3 {
		t.Fatalf("written = %v, want the whole chain", written)
	}
	for _, node := range []ImageMeta{base, mid, tip} {
		lines := readVersionActivity(t, repo, node.ID)
		if len(lines) != 1 || lines[0].Phase != activity.PhaseInitialized {
			t.Fatalf("version %s lines = %+v, want one initialization", node.Description, lines)
		}
	}
}

// TestEnsureVersionActivityDoesNotRenewAnExistingLog keeps repeated
// scans and plain reads from writing anything.
func TestEnsureVersionActivityDoesNotRenewAnExistingLog(t *testing.T) {
	repo := testRepo(t)
	base := versionNode("base")
	tip := incrementalNode(base, "tip")
	seedVersions(t, repo, base, tip)
	if _, err := EnsureVersionActivityLocked(repo, tip.ID, activityClock); err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	before := readVersionActivity(t, repo, base.ID)
	for range 2 {
		written, err := EnsureVersionActivityLocked(repo, tip.ID, activityClock)
		if err != nil {
			t.Fatalf("repeated ensure: %v", err)
		}
		if len(written) != 0 {
			t.Fatalf("repeated ensure wrote %v, want nothing", written)
		}
	}
	after := readVersionActivity(t, repo, base.ID)
	if len(after) != len(before) {
		t.Fatalf("ancestor lines = %d, want %d", len(after), len(before))
	}
}

// TestEnsureVersionActivityRejectsADamagedTail leaves every log alone
// when an ancestor tail cannot be read.
func TestEnsureVersionActivityRejectsADamagedTail(t *testing.T) {
	repo := testRepo(t)
	base := versionNode("base")
	mid := incrementalNode(base, "mid")
	tip := incrementalNode(mid, "tip")
	seedVersions(t, repo, base, mid, tip)
	if err := os.WriteFile(activityLogPath(t, repo, mid.ID), []byte("{\n"), 0o600); err != nil {
		t.Fatalf("damage the tail: %v", err)
	}
	if _, err := EnsureVersionActivityLocked(repo, tip.ID, activityClock); !errors.Is(err, activity.ErrDamagedLog) {
		t.Fatalf("ensure err = %v, want ErrDamagedLog", err)
	}
	requireNoActivityLog(t, repo, base.ID)
	requireNoActivityLog(t, repo, tip.ID)
}

// TestEnsureVersionActivityReportsTheVersionsWrittenBeforeAFailure
// returns the ancestors that were initialized even when the source
// record cannot be written.
func TestEnsureVersionActivityReportsTheVersionsWrittenBeforeAFailure(t *testing.T) {
	repo := testRepo(t)
	base := versionNode("base")
	mid := incrementalNode(base, "mid")
	tip := incrementalNode(mid, "tip")
	seedVersions(t, repo, base, mid, tip)
	boom := errors.New("log boom")
	opens := map[string]int{}
	batch := versionActivityBatch{
		repo:  repo,
		clock: activityClock,
		open: func(repo Repo, id string, clock activity.Clock) (*activity.Writer, error) {
			// The source is opened for its pre-check, for the batch
			// time, and then for the initialization write.
			opens[id]++
			if id == tip.ID && opens[id] > 2 {
				return nil, boom
			}
			dir, err := repo.ObjectDirPath(id)
			if err != nil {
				return nil, err
			}
			return activity.OpenLog(libraryRoot(repo), dir, clock)
		},
	}
	written, err := batch.ensure(tip.ID)
	if !errors.Is(err, boom) {
		t.Fatalf("ensure err = %v, want the injected failure", err)
	}
	if strings.Join(written, ",") != base.ID+","+mid.ID {
		t.Fatalf("written = %v, want the ancestors alone", written)
	}
	for _, node := range []ImageMeta{base, mid} {
		lines := readVersionActivity(t, repo, node.ID)
		if len(lines) != 1 || lines[0].Phase != activity.PhaseInitialized {
			t.Fatalf("version %s lines = %+v, want one initialization", node.Description, lines)
		}
	}
	requireNoActivityLog(t, repo, tip.ID)
}

// TestEnsureVersionActivitySharesTheNewestTailTime stamps the whole
// initialization with a future ancestor time instead of the clock.
func TestEnsureVersionActivitySharesTheNewestTailTime(t *testing.T) {
	repo := testRepo(t)
	base := versionNode("base")
	tip := incrementalNode(base, "tip")
	seedVersions(t, repo, base, tip)
	future := testActivityNow.Add(2 * time.Hour)
	line := fmt.Sprintf("{\"time\":%q,\"event\":%q,\"phase\":%q}\n",
		future.Format(time.RFC3339Nano), activity.EventMount, activity.PhaseSucceeded)
	if err := os.WriteFile(activityLogPath(t, repo, base.ID), []byte(line), 0o600); err != nil {
		t.Fatalf("seed ancestor tail: %v", err)
	}
	if _, err := EnsureVersionActivityLocked(repo, tip.ID, activityClock); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	want := future.Format(time.RFC3339Nano)
	parent := readVersionActivity(t, repo, base.ID)
	if len(parent) != 2 || parent[1].Time != want {
		t.Fatalf("parent lines = %+v, want the initialization at %s", parent, want)
	}
	source := readVersionActivity(t, repo, tip.ID)
	if len(source) != 1 || source[0].Time != want {
		t.Fatalf("source lines = %+v, want the initialization at %s", source, want)
	}
}

// TestEnsureVersionActivityHealsAnInterruptedInitialization finishes
// a propagation that stopped after the source record landed.
func TestEnsureVersionActivityHealsAnInterruptedInitialization(t *testing.T) {
	repo := testRepo(t)
	base := versionNode("base")
	tip := incrementalNode(base, "tip")
	seedVersions(t, repo, base, tip)
	if _, err := EnsureVersionActivityLocked(repo, tip.ID, activityClock); err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	if err := os.Remove(activityLogPath(t, repo, base.ID)); err != nil {
		t.Fatalf("drop the ancestor log: %v", err)
	}
	if written, err := EnsureVersionActivityLocked(repo, tip.ID, activityClock); err != nil || len(written) != 0 {
		t.Fatalf("source ensure = %v, %v, want a no-op", written, err)
	}
	written, err := EnsureVersionActivityLocked(repo, base.ID, activityClock)
	if err != nil {
		t.Fatalf("ancestor ensure: %v", err)
	}
	if strings.Join(written, ",") != base.ID {
		t.Fatalf("written = %v, want the ancestor alone", written)
	}
}

// TestEnsureVersionActivityScanOrderDoesNotChangeTheOutcome initializes
// the same chain from opposite scan orders: every order leaves one
// readable log per version holding only initialization records, and a
// second pass over the whole library writes nothing.
func TestEnsureVersionActivityScanOrderDoesNotChangeTheOutcome(t *testing.T) {
	scan := func(t *testing.T, order []int) map[string]bool {
		t.Helper()
		repo := testRepo(t)
		base := versionNode("base")
		mid := incrementalNode(base, "mid")
		tip := incrementalNode(mid, "tip")
		nodes := []ImageMeta{base, mid, tip}
		seedVersions(t, repo, nodes...)
		touched := make(map[string]bool, len(nodes))
		for _, i := range order {
			written, err := EnsureVersionActivityLocked(repo, nodes[i].ID, activityClock)
			if err != nil {
				t.Fatalf("ensure %s: %v", nodes[i].Description, err)
			}
			for _, id := range written {
				touched[id] = true
			}
		}
		for _, node := range nodes {
			lines := readVersionActivity(t, repo, node.ID)
			if len(lines) == 0 {
				t.Fatalf("version %s has no log after the scan", node.Description)
			}
			for _, line := range lines {
				if line.Phase != activity.PhaseInitialized {
					t.Fatalf("version %s line = %+v, want only initialization", node.Description, line)
				}
			}
			if again, err := EnsureVersionActivityLocked(repo, node.ID, activityClock); err != nil || len(again) != 0 {
				t.Fatalf("rescan of %s = %v, %v, want a no-op", node.Description, again, err)
			}
		}
		return touched
	}
	first := scan(t, []int{0, 1, 2})
	second := scan(t, []int{2, 0, 1})
	if len(first) != 3 || len(second) != 3 {
		t.Fatalf("scans touched %d and %d versions, want the whole chain", len(first), len(second))
	}
	for id := range first {
		if !second[id] {
			t.Fatalf("version %s received no record in the reverse order", id)
		}
	}
}

// TestEnsureVersionActivityCreatesAMissingObjectDirectory initializes a
// legacy version whose object directory no longer exists.
func TestEnsureVersionActivityCreatesAMissingObjectDirectory(t *testing.T) {
	repo := testRepo(t)
	base := versionNode("base")
	tip := incrementalNode(base, "tip")
	seedVersions(t, repo, base, tip)
	for _, node := range []ImageMeta{base, tip} {
		dir, err := repo.ObjectDirPath(node.ID)
		if err != nil {
			t.Fatalf("object dir of %s: %v", node.Description, err)
		}
		if err := os.RemoveAll(dir); err != nil {
			t.Fatalf("remove object dir of %s: %v", node.Description, err)
		}
	}
	written, err := EnsureVersionActivityLocked(repo, tip.ID, activityClock)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if strings.Join(written, ",") != base.ID+","+tip.ID {
		t.Fatalf("written = %v, want the whole chain", written)
	}
	for _, node := range []ImageMeta{base, tip} {
		lines := readVersionActivity(t, repo, node.ID)
		if len(lines) != 1 || lines[0].Phase != activity.PhaseInitialized {
			t.Fatalf("version %s lines = %+v, want one initialization", node.Description, lines)
		}
	}
}

// TestVersionActivityPropagatesAlongTheParentChain records one event
// on the source and one attributed dependency record on every
// ancestor, leaving sibling branches alone.
func TestVersionActivityPropagatesAlongTheParentChain(t *testing.T) {
	repo := testRepo(t)
	base := versionNode("base")
	mid := incrementalNode(base, "mid")
	tip := incrementalNode(mid, "tip")
	sibling := incrementalNode(base, "sibling")
	seedVersions(t, repo, base, mid, tip, sibling)
	ev := activity.Event{
		Event:      activity.EventMount,
		Phase:      activity.PhaseStarted,
		Session:    "dev",
		InstanceID: "instance-1",
		Seq:        7,
		Mountpoint: "/mnt/tip",
	}
	if err := AppendVersionActivityLocked(repo, tip.ID, ev, activityClock); err != nil {
		t.Fatalf("append: %v", err)
	}
	for _, node := range []ImageMeta{base, mid} {
		lines := readVersionActivity(t, repo, node.ID)
		if len(lines) != 1 {
			t.Fatalf("ancestor %s lines = %+v, want one", node.Description, lines)
		}
		line := lines[0]
		if line.Event != activity.EventImageDependency || line.Phase != activity.PhaseStarted {
			t.Fatalf("ancestor %s record = %+v, want a dependency record", node.Description, line)
		}
		if line.Version != node.ID || line.SourceVersion != tip.ID ||
			line.SourceEvent != activity.EventMount {
			t.Fatalf("ancestor %s attribution = %+v", node.Description, line)
		}
		if line.Mountpoint != "/mnt/tip" || line.Image != repo.Name() {
			t.Fatalf("ancestor %s context = %+v", node.Description, line)
		}
		if line.Session != "dev" || line.InstanceID != "instance-1" || line.Seq != 7 {
			t.Fatalf("ancestor %s action context = %+v", node.Description, line)
		}
	}
	source := readVersionActivity(t, repo, tip.ID)
	if len(source) != 1 || source[0].Event != activity.EventMount ||
		source[0].Version != tip.ID || source[0].SourceVersion != "" {
		t.Fatalf("source record = %+v, want the event itself", source)
	}
	if source[0].Session != "dev" || source[0].Seq != 7 {
		t.Fatalf("source action context = %+v", source[0])
	}
	requireNoActivityLog(t, repo, sibling.ID)
}

// TestVersionActivityFromFullRootStaysLocal writes only the full
// root's own log, because a full version has no ancestors.
func TestVersionActivityFromFullRootStaysLocal(t *testing.T) {
	repo := testRepo(t)
	base := versionNode("base")
	child := incrementalNode(base, "child")
	seedVersions(t, repo, base, child)
	ev := activity.Event{Event: activity.EventImageCreateFull, Phase: activity.PhaseSucceeded}
	if err := AppendVersionActivityLocked(repo, base.ID, ev, activityClock); err != nil {
		t.Fatalf("append: %v", err)
	}
	lines := readVersionActivity(t, repo, base.ID)
	if len(lines) != 1 || lines[0].Event != activity.EventImageCreateFull ||
		lines[0].SourceVersion != "" || lines[0].SourceEvent != "" {
		t.Fatalf("full root record = %+v, want the event itself", lines)
	}
	requireNoActivityLog(t, repo, child.ID)
}

// TestVersionActivitySharesTheNewestTailTime stamps the whole batch
// with a future ancestor time instead of the clock.
func TestVersionActivitySharesTheNewestTailTime(t *testing.T) {
	repo := testRepo(t)
	base := versionNode("base")
	tip := incrementalNode(base, "tip")
	seedVersions(t, repo, base, tip)
	future := testActivityNow.Add(2 * time.Hour)
	line := fmt.Sprintf("{\"time\":%q,\"event\":%q,\"phase\":%q}\n",
		future.Format(time.RFC3339Nano), activity.EventMount, activity.PhaseSucceeded)
	if err := os.WriteFile(activityLogPath(t, repo, base.ID), []byte(line), 0o600); err != nil {
		t.Fatalf("seed ancestor tail: %v", err)
	}
	ev := activity.Event{Event: activity.EventMount, Phase: activity.PhaseSucceeded}
	if err := AppendVersionActivityLocked(repo, tip.ID, ev, activityClock); err != nil {
		t.Fatalf("append: %v", err)
	}
	want := future.Format(time.RFC3339Nano)
	for _, node := range []ImageMeta{base, tip} {
		lines := readVersionActivity(t, repo, node.ID)
		last := lines[len(lines)-1]
		if last.Time != want {
			t.Fatalf("version %s time = %q, want %q", node.Description, last.Time, want)
		}
	}
}

// TestVersionActivityRejectsADamagedTailBeforeWriting fails the batch
// on an unreadable ancestor tail and leaves every log alone.
func TestVersionActivityRejectsADamagedTailBeforeWriting(t *testing.T) {
	repo := testRepo(t)
	base := versionNode("base")
	mid := incrementalNode(base, "mid")
	tip := incrementalNode(mid, "tip")
	seedVersions(t, repo, base, mid, tip)
	if err := os.WriteFile(activityLogPath(t, repo, mid.ID), []byte(`{"time":"broken`), 0o600); err != nil {
		t.Fatalf("damage the tail: %v", err)
	}
	ev := activity.Event{Event: activity.EventMount, Phase: activity.PhaseStarted}
	err := AppendVersionActivityLocked(repo, tip.ID, ev, activityClock)
	if !errors.Is(err, activity.ErrDamagedLog) {
		t.Fatalf("append err = %v, want ErrDamagedLog", err)
	}
	requireNoActivityLog(t, repo, base.ID)
	requireNoActivityLog(t, repo, tip.ID)
}

// TestVersionActivityRejectsABrokenChain reports unknown sources,
// missing parents, and cycles without writing any log.
func TestVersionActivityRejectsABrokenChain(t *testing.T) {
	ev := activity.Event{Event: activity.EventMount, Phase: activity.PhaseStarted}
	t.Run("unknown source", func(t *testing.T) {
		repo := testRepo(t)
		base := versionNode("base")
		seedVersions(t, repo, base)
		if err := AppendVersionActivityLocked(repo, "deadbeef", ev, activityClock); err == nil {
			t.Fatal("append accepted an unknown source")
		}
		requireNoActivityLog(t, repo, base.ID)
	})
	for _, failure := range []string{"missing parent", "cycle"} {
		t.Run(failure, func(t *testing.T) {
			repo := testRepo(t)
			base := versionNode("base")
			child := incrementalNode(base, "child")
			seedVersions(t, repo, base, child)
			manifest, err := LoadVersionManifest(repo)
			if err != nil {
				t.Fatal(err)
			}
			switch failure {
			case "missing parent":
				manifest.Images[1].Parent = "deadbeef"
			case "cycle":
				manifest.Images[1].Parent = manifest.Images[1].ID
			}
			body, err := yaml.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(repo.MetaPath(), body, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := AppendVersionActivityLocked(repo, child.ID, ev, activityClock); err == nil {
				t.Fatalf("append accepted a %s", failure)
			}
			requireNoActivityLog(t, repo, base.ID)
			requireNoActivityLog(t, repo, child.ID)
		})
	}
}

// TestVersionActivityStopsAtMidChainFailure keeps the root record
// written by a batch that fails below it.
func TestVersionActivityStopsAtMidChainFailure(t *testing.T) {
	repo := testRepo(t)
	base := versionNode("base")
	mid := incrementalNode(base, "mid")
	tip := incrementalNode(mid, "tip")
	seedVersions(t, repo, base, mid, tip)
	boom := errors.New("log boom")
	opens := map[string]int{}
	batch := versionActivityBatch{
		repo:  repo,
		clock: activityClock,
		open: func(repo Repo, id string, clock activity.Clock) (*activity.Writer, error) {
			// The first open of a version reads its tail; failing the
			// second one breaks the batch after the root record landed.
			opens[id]++
			if id == mid.ID && opens[id] > 1 {
				return nil, boom
			}
			dir, err := repo.ObjectDirPath(id)
			if err != nil {
				return nil, err
			}
			return activity.OpenLog(libraryRoot(repo), dir, clock)
		},
	}
	ev := activity.Event{Event: activity.EventMount, Phase: activity.PhaseStarted}
	err := batch.append(tip.ID, ev)
	if !errors.Is(err, boom) {
		t.Fatalf("append err = %v, want the injected failure", err)
	}
	if !strings.Contains(err.Error(), mid.ID) || !strings.Contains(err.Error(), activity.PhaseStarted) {
		t.Fatalf("append err = %v, want the version and phase", err)
	}
	if lines := readVersionActivity(t, repo, base.ID); len(lines) != 1 {
		t.Fatalf("root lines = %+v, want the conservative record", lines)
	}
	requireNoActivityLog(t, repo, mid.ID)
	requireNoActivityLog(t, repo, tip.ID)
}

// TestVersionActivityBatchesSerializeUnderTheLibraryLock keeps
// concurrent batches from interleaving or losing records.
func TestVersionActivityBatchesSerializeUnderTheLibraryLock(t *testing.T) {
	repo := testRepo(t)
	base := versionNode("base")
	tip := incrementalNode(base, "tip")
	seedVersions(t, repo, base, tip)
	const writers = 4
	var wg sync.WaitGroup
	errs := make([]error, writers)
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			hold, err := LockImage(libraryRoot(repo), repo.Name(), time.Second)
			if err != nil {
				errs[i] = err
				return
			}
			defer hold.Release()
			errs[i] = AppendVersionActivityLocked(repo, tip.ID, activity.Event{
				Event:      activity.EventMount,
				Phase:      activity.PhaseSucceeded,
				Mountpoint: fmt.Sprintf("/mnt/%d", i),
			}, activityClock)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("writer %d: %v", i, err)
		}
	}
	for _, node := range []ImageMeta{base, tip} {
		lines := readVersionActivity(t, repo, node.ID)
		if len(lines) != writers {
			t.Fatalf("version %s lines = %d, want %d", node.Description, len(lines), writers)
		}
	}
}

// requireCreated asserts the only record of a freshly published version.
func requireCreated(t *testing.T, repo Repo, id, event string) {
	t.Helper()
	lines := readVersionActivity(t, repo, id)
	if len(lines) != 1 || lines[0].Event != event || lines[0].Phase != activity.PhaseSucceeded {
		t.Fatalf("version %s lines = %+v, want one successful %s", id, lines, event)
	}
	if lines[0].Version != id || lines[0].Image != repo.Name() || lines[0].SourceVersion != "" {
		t.Fatalf("version %s attribution = %+v", id, lines[0])
	}
}

// publishNode publishes text as a new version under its pinned parent.
func publishNode(t *testing.T, repo Repo, node ImageMeta, parent *ImageMeta) ImageMeta {
	t.Helper()
	stage := writeSource(t, t.TempDir(), []byte(node.Description))
	root := linkImagesRoot(repo)
	got, err := PublishFullVersion(root, repo.Name(), stage, node, false)
	if parent != nil {
		got, err = PublishIncrementalVersion(root, repo.Name(), stage, node, *parent, false)
	}
	if err != nil {
		t.Fatalf("publish %s: %v", node.Description, err)
	}
	return got
}

// TestPublishActivityRecordsAFullCreation records the creation of a
// published full version in the version's own log.
func TestPublishActivityRecordsAFullCreation(t *testing.T) {
	repo := testRepo(t)
	base := versionNode("base")
	publishNode(t, repo, base, nil)
	requireCreated(t, repo, base.ID, activity.EventImageCreateFull)
}

// TestPublishActivityPropagatesIncrementalCreation attributes the phase
// that reads a parent chain to the parent, the creation to the new
// version, and appends one record per ancestor and phase.
func TestPublishActivityPropagatesIncrementalCreation(t *testing.T) {
	repo := testRepo(t)
	base := publishNode(t, repo, versionNode("base"), nil)
	mid := incrementalNode(base, "mid")
	publishNode(t, repo, mid, &base)
	tip := incrementalNode(mid, "tip")
	publishNode(t, repo, tip, &mid)
	source := readVersionActivity(t, repo, tip.ID)
	if len(source) != 1 || source[0].Event != activity.EventImageCreateIncremental ||
		source[0].Phase != activity.PhaseSucceeded || source[0].SourceVersion != "" {
		t.Fatalf("tip lines = %+v, want its own creation", source)
	}
	want := []struct{ event, phase, source string }{
		{activity.EventImageCreateIncremental, activity.PhaseSucceeded, ""},
		{activity.EventImageCreateIncremental, activity.PhaseStarted, ""},
		{activity.EventImageDependency, activity.PhaseSucceeded, tip.ID},
	}
	parent := readVersionActivity(t, repo, mid.ID)
	if len(parent) != len(want) {
		t.Fatalf("mid lines = %+v, want one record per phase", parent)
	}
	for i, expected := range want {
		line := parent[i]
		if line.Event != expected.event || line.Phase != expected.phase || line.SourceVersion != expected.source {
			t.Fatalf("mid line %d = %+v, want %+v", i, line, expected)
		}
	}
	root := readVersionActivity(t, repo, base.ID)
	if len(root) != 5 {
		t.Fatalf("root lines = %+v, want one record per phase of each publication", root)
	}
	if root[0].Event != activity.EventImageCreateFull {
		t.Fatalf("root creation = %+v", root[0])
	}
	if root[3].Event != activity.EventImageDependency || root[3].Phase != activity.PhaseStarted ||
		root[3].SourceVersion != mid.ID || root[3].SourceEvent != activity.EventImageCreateIncremental {
		t.Fatalf("root parent-chain read = %+v, want the mid publication", root[3])
	}
	if root[4].Event != activity.EventImageDependency || root[4].Phase != activity.PhaseSucceeded ||
		root[4].SourceVersion != tip.ID || root[4].SourceEvent != activity.EventImageCreateIncremental {
		t.Fatalf("root creation dependency = %+v, want the tip publication", root[4])
	}
}

// TestPublishActivityRecordsAReuseInsteadOfACreation keeps one creation
// record and adds a use record for the repeated publication.
func TestPublishActivityRecordsAReuseInsteadOfACreation(t *testing.T) {
	repo := testRepo(t)
	base := publishNode(t, repo, versionNode("base"), nil)
	publishNode(t, repo, base, nil)
	tip := incrementalNode(base, "tip")
	publishNode(t, repo, tip, &base)
	publishNode(t, repo, tip, &base)
	source := readVersionActivity(t, repo, tip.ID)
	if len(source) != 2 || source[0].Event != activity.EventImageCreateIncremental ||
		source[1].Event != activity.EventImageReuse || source[1].Phase != activity.PhaseSucceeded {
		t.Fatalf("tip lines = %+v, want the creation and the reuse", source)
	}
	root := readVersionActivity(t, repo, base.ID)
	if len(root) != 5 {
		t.Fatalf("root lines = %+v, want one record per phase", root)
	}
	if root[1].Event != activity.EventImageReuse || root[1].Phase != activity.PhaseSucceeded {
		t.Fatalf("root reuse = %+v", root[1])
	}
	last := root[len(root)-1]
	if last.Event != activity.EventImageDependency || last.SourceVersion != tip.ID ||
		last.SourceEvent != activity.EventImageReuse {
		t.Fatalf("root reuse dependency = %+v, want the tip reuse", last)
	}
}

// TestPublishActivityAbortsOnADamagedParentTail keeps the manifest
// unchanged when the parent chain cannot be read.
func TestPublishActivityAbortsOnADamagedParentTail(t *testing.T) {
	repo := testRepo(t)
	base := publishNode(t, repo, versionNode("base"), nil)
	if err := os.WriteFile(activityLogPath(t, repo, base.ID), []byte(`{"time":"broken`), 0o600); err != nil {
		t.Fatalf("damage the parent tail: %v", err)
	}
	tip := incrementalNode(base, "tip")
	stage := writeSource(t, t.TempDir(), []byte("tip"))
	root := linkImagesRoot(repo)
	if _, err := PublishIncrementalVersion(root, repo.Name(), stage, tip, base, false); !errors.Is(err, activity.ErrDamagedLog) {
		t.Fatalf("publish err = %v, want ErrDamagedLog", err)
	}
	manifest, err := LoadVersionManifest(repo)
	if err != nil || len(manifest.Images) != 1 {
		t.Fatalf("damaged activity published %d versions: %v", len(manifest.Images), err)
	}
	requireNoActivityLog(t, repo, tip.ID)
}

// TestPublishActivityReportsAFailedCreationWrite keeps the published
// version and reports the missing record: the manifest is authoritative.
func TestPublishActivityReportsAFailedCreationWrite(t *testing.T) {
	repo := testRepo(t)
	base := versionNode("base")
	logPath := activityLogPath(t, repo, base.ID)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatalf("create object dir: %v", err)
	}
	damaged := []byte(`{"time":"broken`)
	if err := os.WriteFile(logPath, damaged, 0o600); err != nil {
		t.Fatalf("damage the creation log: %v", err)
	}
	stage := writeSource(t, t.TempDir(), []byte("base"))
	got, err := PublishFullVersion(linkImagesRoot(repo), repo.Name(), stage, base, false)
	if !errors.Is(err, activity.ErrDamagedLog) {
		t.Fatalf("publish err = %v, want ErrDamagedLog", err)
	}
	if got.ID != base.ID {
		t.Fatalf("published version = %#v, want the committed version", got)
	}
	manifest, err := LoadVersionManifest(repo)
	if err != nil || len(manifest.Images) != 1 || manifest.Images[0].ID != base.ID {
		t.Fatalf("manifest = %#v, %v, want the published version", manifest, err)
	}
	body, err := os.ReadFile(logPath)
	if err != nil || string(body) != string(damaged) {
		t.Fatalf("log = %q, %v, want the damaged tail untouched", body, err)
	}
}

// resolvedStack returns the manifest's root-to-tip stack of one
// version, which is the chain a mount boundary hands to the stack
// activity entry point.
func resolvedStack(t *testing.T, repo Repo, tip string) []ImageMeta {
	t.Helper()
	manifest, err := LoadVersionManifest(repo)
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	stack, err := manifest.SelectVersion(repo.Name() + ":" + tip)
	if err != nil {
		t.Fatalf("select stack of %s: %v", tip, err)
	}
	return stack
}

// TestStackActivityRecordsTheTipAndEachAncestorOnce records one mount
// phase for a resolved F-A-B chain: the tip carries the event, every
// ancestor one dependency record, and the untouched sibling of the root
// records nothing.
func TestStackActivityRecordsTheTipAndEachAncestorOnce(t *testing.T) {
	repo := testRepo(t)
	base := versionNode("base")
	mid := incrementalNode(base, "mid")
	tip := incrementalNode(mid, "tip")
	sibling := incrementalNode(base, "sibling")
	seedVersions(t, repo, base, mid, tip, sibling)
	stack := resolvedStack(t, repo, tip.ID)
	if len(stack) != 3 || stack[0].ID != base.ID || stack[2].ID != tip.ID {
		t.Fatalf("resolved stack = %#v", stack)
	}
	for _, phase := range []string{activity.PhaseStarted, activity.PhaseSucceeded} {
		ev := activity.Event{Event: activity.EventMount, Phase: phase,
			Session: "dev", Seq: 2, Mountpoint: "/view"}
		if err := AppendVersionStackActivityLocked(repo, tip.ID, stack, ev, activityClock); err != nil {
			t.Fatalf("append %s: %v", phase, err)
		}
	}
	wantPhases := []string{activity.PhaseStarted, activity.PhaseSucceeded}
	for _, node := range []ImageMeta{base, mid, tip} {
		lines := readVersionActivity(t, repo, node.ID)
		if len(lines) != len(wantPhases) {
			t.Fatalf("version %s lines = %+v, want one record per phase",
				node.Description, lines)
		}
		for i, line := range lines {
			if line.Phase != wantPhases[i] || line.Version != node.ID ||
				line.Session != "dev" || line.Seq != 2 || line.Mountpoint != "/view" {
				t.Fatalf("version %s line %d = %+v", node.Description, i, line)
			}
			if line.Time != lines[0].Time {
				t.Fatalf("version %s lines = %+v, want one batch instant", node.Description, lines)
			}
			if node.ID == tip.ID {
				if line.Event != activity.EventMount || line.SourceVersion != "" {
					t.Fatalf("tip line %d = %+v, want the mount event itself", i, line)
				}
				continue
			}
			if line.Event != activity.EventImageDependency || line.SourceVersion != tip.ID ||
				line.SourceEvent != activity.EventMount || line.Image != repo.Name() {
				t.Fatalf("ancestor %s line %d = %+v, want a dependency on the tip",
					node.Description, i, line)
			}
		}
	}
	requireNoActivityLog(t, repo, sibling.ID)
}

// TestStackActivityRejectsAChainThatDriftedFromTheManifest refuses a
// chain the forest does not publish, and records nothing for it.
func TestStackActivityRejectsAChainThatDriftedFromTheManifest(t *testing.T) {
	repo := testRepo(t)
	base := versionNode("base")
	mid := incrementalNode(base, "mid")
	tip := incrementalNode(mid, "tip")
	seedVersions(t, repo, base, mid, tip)
	stack := resolvedStack(t, repo, tip.ID)
	ev := activity.Event{Event: activity.EventMount, Phase: activity.PhaseSucceeded}
	cases := map[string]struct {
		tip   string
		chain []ImageMeta
	}{
		"empty chain":  {tip.ID, nil},
		"foreign tip":  {mid.ID, stack},
		"short chain":  {tip.ID, stack[2:]},
		"reordered":    {tip.ID, []ImageMeta{mid, base, tip}},
		"dropped root": {tip.ID, stack[1:]},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := AppendVersionStackActivityLocked(repo, tc.tip, tc.chain, ev, activityClock)
			if err == nil {
				t.Fatalf("append accepted a %s", name)
			}
			for _, node := range []ImageMeta{base, mid, tip} {
				requireNoActivityLog(t, repo, node.ID)
			}
		})
	}
}

// TestPublishActivityCoversEveryPublicationPath records a creation from
// the split, imported external, and caller-locked entry points.
func TestPublishActivityCoversEveryPublicationPath(t *testing.T) {
	t.Run("split", func(t *testing.T) {
		repo := testRepo(t)
		stage, node := stagedSplitVersion(t, "payload", 3)
		if _, err := PublishFullVersion(linkImagesRoot(repo), repo.Name(), stage, node, false); err != nil {
			t.Fatalf("publish split version: %v", err)
		}
		requireCreated(t, repo, node.ID, activity.EventImageCreateFull)
	})
	t.Run("external link", func(t *testing.T) {
		withLinuxLinkPolicy(t)
		images, nas := t.TempDir(), t.TempDir()
		src := writeFakeSquashfs(t, nas, []byte("vendor-payload"))
		if err := LinkExternal(images, "alice/app", src, "test-builder"); err != nil {
			t.Fatalf("link external: %v", err)
		}
		repo, err := OpenRepo(images, "alice/app")
		if err != nil {
			t.Fatalf("open repo: %v", err)
		}
		manifest, err := LoadVersionManifest(repo)
		if err != nil || len(manifest.Images) != 1 {
			t.Fatalf("manifest = %#v, %v", manifest, err)
		}
		id := manifest.Images[0].ID
		requireCreated(t, repo, id, activity.EventImageCreateFull)
		if err := LinkExternal(images, "alice/app", src, "test-builder"); err != nil {
			t.Fatalf("repeat link: %v", err)
		}
		lines := readVersionActivity(t, repo, id)
		if len(lines) != 2 || lines[1].Event != activity.EventImageReuse {
			t.Fatalf("linked version lines = %+v, want the creation and the reuse", lines)
		}
	})
	t.Run("caller locked", func(t *testing.T) {
		repo := testRepo(t)
		root := linkImagesRoot(repo)
		base := versionNode("base")
		hold, err := LockImage(root, repo.Name(), 0)
		if err != nil {
			t.Fatalf("lock image: %v", err)
		}
		defer hold.Release()
		stage := writeSource(t, t.TempDir(), []byte("base"))
		if _, err := PublishFullVersionLocked(root, repo.Name(), stage, base, false); err != nil {
			t.Fatalf("publish under the caller lock: %v", err)
		}
		requireCreated(t, repo, base.ID, activity.EventImageCreateFull)
		tip := incrementalNode(base, "tip")
		stage = writeSource(t, t.TempDir(), []byte("tip"))
		if _, err := PublishIncrementalVersionLocked(root, repo.Name(), stage, tip, base, false); err != nil {
			t.Fatalf("publish incremental under the caller lock: %v", err)
		}
		requireCreated(t, repo, tip.ID, activity.EventImageCreateIncremental)
		parent := readVersionActivity(t, repo, base.ID)
		if len(parent) != 3 {
			t.Fatalf("parent lines = %+v, want the creation, its parent-chain read, and the dependency", parent)
		}
		if last := parent[2]; last.Event != activity.EventImageDependency ||
			last.SourceVersion != tip.ID || last.Phase != activity.PhaseSucceeded {
			t.Fatalf("parent dependency = %+v, want the published tip", last)
		}
	})
}
