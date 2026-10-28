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

// seedVersions publishes metadata-only versions and creates the object
// directory each payload and activity log lives in.
func seedVersions(t *testing.T, repo Repo, nodes ...ImageMeta) {
	t.Helper()
	for _, node := range nodes {
		if _, err := appendVersion(repo, node, false, hooks{}); err != nil {
			t.Fatalf("append %s: %v", node.Description, err)
		}
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
			release, err := LockLibrary(libraryRoot(repo), time.Second)
			if err != nil {
				errs[i] = err
				return
			}
			defer release()
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
