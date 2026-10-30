package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/activity"
	"github.com/jiangxin/tiler/internal/image"
	"github.com/jiangxin/tiler/internal/session"
)

func imageGCArgs(lib []string, extra ...string) []string {
	return append(append(append([]string{}, lib...), "images", "gc"), extra...)
}

func imageGCFixture(t *testing.T) (string, []string, image.Repo, []image.ImageMeta) {
	t.Helper()
	home, _ := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	old := time.Now().UTC().Add(-90 * expireDay)
	nodes := []image.ImageMeta{versionNode("a", image.KindFull, "", old), versionNode("b", image.KindFull, "", old)}
	writeVersionForest(t, imagesDir, "alice/app", nodes...)
	repo, _ := image.OpenRepo(imagesDir, "alice/app")
	for _, node := range nodes {
		writeImmutablePayload(t, repo, node, []byte("x"))
		seedImageGCActivity(t, imagesDir, repo, node.ID, old)
	}
	return home, lib, repo, nodes
}

func seedImageGCActivity(t *testing.T, imagesDir string, repo image.Repo, id string, at time.Time) {
	t.Helper()
	logs := filepath.Join(filepath.Dir(filepath.Dir(imagesDir)), ".tiler", "logs")
	dir := filepath.Join(logs, "images", filepath.FromSlash(repo.Name()))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	w, err := activity.OpenLog(logs, dir, id+".log", "", func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Append(activity.Event{Event: activity.EventMount, Phase: activity.PhaseSucceeded,
		Image: repo.Name(), Version: id}); err != nil {
		t.Fatal(err)
	}
}

func assertImageGCObjects(t *testing.T, repo image.Repo, nodes []image.ImageMeta, exists bool) {
	t.Helper()
	for _, node := range nodes {
		dir, _ := repo.ObjectDirPath(node.ID)
		_, err := os.Stat(dir)
		if (err == nil) != exists || (err != nil && !os.IsNotExist(err)) {
			t.Fatalf("object %s exists=%v: %v", node.ID, exists, err)
		}
	}
}

func TestImagesGCHelpAndUsage(t *testing.T) {
	_, _ = withWorkspace(t)
	for _, command := range []string{"images", "image"} {
		stdout, stderr, _, err := runCmd(t, command, "gc", "--help")
		if err != nil {
			t.Fatalf("help: %v\n%s", err, stderr)
		}
		for _, text := range []string{"--expire", "default 30", "--yes", "orphan", "never removes"} {
			if !strings.Contains(stdout, text) {
				t.Fatalf("help omitted %q: %s", text, stdout)
			}
		}
	}
	for _, args := range [][]string{{"extra"}, {"--expire=-1"}, {"--expire=1.5"}, {"--expire=abc"},
		{"--expire=" + strconv.FormatInt(maxExpireDays+1, 10)}} {
		_, _, _, err := runCmd(t, append([]string{"images", "gc"}, args...)...)
		if ExitCode(err) != 2 {
			t.Fatalf("args %v error=%v, exit=%d", args, err, ExitCode(err))
		}
	}
}

func TestImagesGCPreviewNeverConsumesPipedYesOrDeletes(t *testing.T) {
	_, lib, repo, nodes := imageGCFixture(t)
	withTerminalSeam(t, false)
	stdout, stderr, err := runCmdReader(t, failReader{errors.New("must not read")}, imageGCArgs(lib)...)
	if err != nil {
		t.Fatalf("preview: %v\n%s", err, stderr)
	}
	for _, node := range nodes {
		if !strings.Contains(stdout, "expired  alice/app:"+node.ID) {
			t.Fatalf("missing full ID: %s", stdout)
		}
	}
	if !strings.Contains(stdout, "last active") || !strings.Contains(stdout, "age 90d") ||
		strings.Contains(stdout, "removed") || !strings.Contains(stderr, "--yes") {
		t.Fatalf("preview stdout=%q stderr=%q", stdout, stderr)
	}
	assertImageGCObjects(t, repo, nodes, true)
}

func TestImagesGCRequiresExactInteractiveYes(t *testing.T) {
	for _, answer := range []string{"yes\n", "y\n", "YES\n", " yes\n", "yes \n", "\n", "yes", ""} {
		t.Run(strconv.Quote(answer), func(t *testing.T) {
			_, lib, repo, nodes := imageGCFixture(t)
			withTerminalSeam(t, true)
			stdout, stderr, err := runCmdInput(t, answer, imageGCArgs(lib)...)
			if err != nil {
				t.Fatalf("confirmation: %v\n%s", err, stderr)
			}
			removed := answer == "yes\n"
			assertImageGCObjects(t, repo, nodes, !removed)
			if strings.Count(stderr, "Type yes") != 1 || strings.Contains(stdout, "removed 2 of 2") != removed {
				t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
			}
		})
	}
}

func TestImagesGCYesFlagListsBeforeRemovingThroughAlias(t *testing.T) {
	_, lib, repo, nodes := imageGCFixture(t)
	withTerminalSeam(t, false)
	args := append(append([]string{}, lib...), "image", "gc", "--yes")
	stdout, stderr, err := runCmdReader(t, failReader{errors.New("must not read")}, args...)
	if err != nil {
		t.Fatalf("--yes: %v\n%s", err, stderr)
	}
	listing, removal := strings.Index(stdout, "expired  alice/app:"), strings.Index(stdout, "removed alice/app:")
	if listing < 0 || removal <= listing || !strings.Contains(stdout, "removed 2 of 2 approved image objects, skipped 0") ||
		strings.Contains(stderr, "Type yes") {
		t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
	}
	assertImageGCObjects(t, repo, nodes, false)
}

func TestImagesGCEmptyPlanDoesNotAsk(t *testing.T) {
	home, _ := withWorkspace(t)
	_, lib := libFlags(home)
	withTerminalSeam(t, true)
	stdout, stderr, err := runCmdReader(t, failReader{errors.New("must not read")}, imageGCArgs(lib, "--yes")...)
	if err != nil || !strings.Contains(stdout, "No image objects eligible") || strings.Contains(stderr, "Type yes") {
		t.Fatalf("stdout=%q stderr=%q error=%v", stdout, stderr, err)
	}
}

func TestImagesGCStreamFailuresPreventDeletion(t *testing.T) {
	for _, where := range []string{"preview", "prompt", "input"} {
		t.Run(where, func(t *testing.T) {
			_, lib, repo, nodes := imageGCFixture(t)
			withTerminalSeam(t, true)
			failure := errors.New("stream unavailable")
			cmd := NewRootCmd()
			cmd.SetArgs(imageGCArgs(lib))
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetIn(strings.NewReader("yes\n"))
			switch where {
			case "preview":
				cmd.SetOut(failWriter{failure})
				cmd.SetArgs(imageGCArgs(lib, "--yes"))
			case "prompt":
				cmd.SetErr(failWriter{failure})
			case "input":
				cmd.SetIn(failReader{failure})
			}
			if err := cmd.Execute(); !errors.Is(err, failure) {
				t.Fatalf("error=%v", err)
			}
			assertImageGCObjects(t, repo, nodes, true)
		})
	}
}

func TestImagesGCRechecksChangesAfterConfirmation(t *testing.T) {
	_, lib, repo, nodes := imageGCFixture(t)
	withTerminalSeam(t, true)
	reader := &confirmReader{answer: "yes\n", mutate: func() {
		seedImageGCActivity(t, filepath.Dir(filepath.Dir(repo.Dir)), repo, nodes[0].ID, time.Now().UTC())
	}}
	stdout, stderr, err := runCmdReader(t, reader, imageGCArgs(lib)...)
	if err != nil || !strings.Contains(stdout, "skipped alice/app:"+nodes[0].ID+": changed") ||
		!strings.Contains(stdout, "removed 1 of 2 approved image objects, skipped 1") {
		t.Fatalf("stdout=%q stderr=%q error=%v", stdout, stderr, err)
	}
	assertImageGCObjects(t, repo, nodes[:1], true)
	assertImageGCObjects(t, repo, nodes[1:], false)
}

func TestImagesGCNeverTouchesSessionRecordsOrRuntime(t *testing.T) {
	home, lib, repo, nodes := imageGCFixture(t)
	store := session.Store{Dir: sessionsDir(home)}
	for _, state := range []string{session.StateActive, session.StateClosed} {
		rec, err := session.NewVersionRecord(state, time.Now().Add(-90*expireDay))
		if err != nil {
			t.Fatal(err)
		}
		rec.State, rec.RemoveOnClose = state, true
		if state == session.StateClosed {
			rec.ClosedAt = time.Now().Add(-90 * expireDay)
		}
		if err := store.SaveVersion(rec); err != nil {
			t.Fatal(err)
		}
		writeTestFile(t, filepath.Join(store.Dir, state, "runtime", "m1", "upper", "work"), "keep")
	}
	before := make(map[string][]byte)
	for _, state := range []string{session.StateActive, session.StateClosed} {
		path := filepath.Join(store.Dir, state, "session.json")
		before[path], _ = os.ReadFile(path)
	}
	stdout := mustGC(t, lib, "--yes", "--session-max-age=1h", "--session-autoclose-stale")
	if !strings.Contains(stdout, "removed 2 of 2") {
		t.Fatalf("output=%q", stdout)
	}
	assertImageGCObjects(t, repo, nodes, false)
	for path, body := range before {
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(body, after) {
			t.Fatalf("session changed: %s: %v", path, err)
		}
		dir := filepath.Dir(path)
		if _, err := os.Stat(filepath.Join(dir, "runtime", "m1", "upper", "work")); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(dir, activity.LogName)); !os.IsNotExist(err) {
			t.Fatalf("image GC initialized session activity: %v", err)
		}
	}
}

func TestImagesGCInitializesChildAndAncestorsOnlyOnce(t *testing.T) {
	home, lib, repo, nodes := imageGCFixture(t)
	imagesDir, _ := libFlags(home)
	root := nodes[0]
	child := versionNode("c", image.KindIncremental, root.ID, root.CreatedAt.Add(time.Hour))
	writeVersionForest(t, imagesDir, repo.Name(), root, child, nodes[1])
	writeImmutablePayload(t, repo, child, []byte("x"))
	withTerminalSeam(t, false)
	stdout, stderr, _, err := runCmd(t, imageGCArgs(lib, "--expire=0")...)
	if err != nil || strings.Contains(stdout, "expired  alice/app:"+root.ID) ||
		strings.Contains(stdout, "expired  alice/app:"+child.ID) || !strings.Contains(stderr, "initialized") {
		t.Fatalf("initial scan stdout=%q stderr=%q error=%v", stdout, stderr, err)
	}
	logs := map[string][]byte{}
	logDir := filepath.Join(home, ".tiler", "logs", "images", "alice", "app")
	for _, node := range []image.ImageMeta{root, child} {
		path := filepath.Join(logDir, node.ID+".log")
		logs[path], err = os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	if !strings.Contains(string(logs[filepath.Join(logDir, root.ID+".log")]), "image.dependency.activity") {
		t.Fatal("child initialization did not propagate")
	}
	if _, stderr, _, err := runCmd(t, imageGCArgs(lib, "--expire=0")...); err != nil || strings.Contains(stderr, "initialized") {
		t.Fatalf("repeat scan stderr=%q error=%v", stderr, err)
	}
	for path, before := range logs {
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("repeat scan renewed %s: %v", path, err)
		}
	}
}

func TestImagesGCLabelsOrphansAndPendingCleanupWithoutPreviewDeletion(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(strconv.FormatBool(pending), func(t *testing.T) {
			home, lib, repo, nodes := imageGCFixture(t)
			imagesDir, _ := libFlags(home)
			var target string
			if pending {
				before := image.VersionManifest{Version: image.VersionManifestVersion, Name: repo.Name(), Images: nodes}
				after := image.VersionManifest{Version: image.VersionManifestVersion, Name: repo.Name()}
				body, err := json.Marshal(map[string]any{"name": repo.Name(), "before": before, "after": after,
					"ids": []string{nodes[0].ID, nodes[1].ID}, "delete_repository": true})
				if err != nil {
					t.Fatal(err)
				}
				target = filepath.Join(imagesDir, ".version-removal.json")
				writeTestFile(t, target, string(body))
			} else {
				target, _ = repo.ObjectDirPath(strings.Repeat("c", 64))
				writeTestFile(t, filepath.Join(target, "payload"), "orphan")
			}
			withTerminalSeam(t, false)
			stdout := mustGC(t, lib)
			label := "orphan"
			if pending {
				label = "pending-cleanup"
				if strings.Contains(stdout, "expired  ") || !strings.Contains(stdout, "rerun images gc") {
					t.Fatalf("pending cleanup expanded expiration: %q", stdout)
				}
			}
			if !strings.Contains(stdout, label+"  alice/app:") || strings.Contains(stdout, "removed") {
				t.Fatalf("preview=%q", stdout)
			}
			if _, err := os.Stat(target); err != nil {
				t.Fatalf("preview removed cleanup target: %v", err)
			}
			if out := mustGC(t, lib, "--yes"); !strings.Contains(out, "removed alice/app:") {
				t.Fatalf("confirmed output=%q", out)
			}
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Fatalf("confirmed cleanup target remains: %v", err)
			}
		})
	}
}

func TestImageGCPreviewHeadsCleanupSeparately(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	cutoff := now.Add(-30 * expireDay)
	id := strings.Repeat("a", 64)
	cleanupOnly := imageGCPreview(session.ImageGCPlan{Cleanup: image.CleanupPlan{Items: []image.CleanupItem{
		{Name: "alice/app", Kind: "pending-cleanup"},
		{Name: "alice/app", ID: id, Kind: "orphan"},
	}}}, now, cutoff)
	var listing bytes.Buffer
	if err := cleanupOnly.write(&listing); err != nil {
		t.Fatal(err)
	}
	text := listing.String()
	if strings.Contains(text, "last activity before") || strings.Contains(text, "pending-cleanup  alice/app:") ||
		!strings.Contains(text, "Image GC cleanup candidates:") || !strings.Contains(text, "orphan  alice/app:"+id) ||
		!strings.Contains(text, "pending-cleanup  alice/app\n") {
		t.Fatalf("cleanup listing %q", text)
	}
	expired := imageGCPreview(session.ImageGCPlan{Versions: []image.VersionGCPlan{{
		Name: "alice/app",
		Candidates: []image.VersionGCCandidate{{
			Meta: image.ImageMeta{ID: strings.Repeat("b", 64)}, Activity: activity.Revision{Last: now.Add(-40 * expireDay)},
		}},
	}}}, now, cutoff)
	if !strings.Contains(expired.Title, "last activity before "+formatGCStamp(cutoff)) || len(expired.Sections) != 0 {
		t.Fatalf("expired title %q", expired.Title)
	}
	mixed := imageGCPreview(session.ImageGCPlan{
		Cleanup: image.CleanupPlan{Items: []image.CleanupItem{{Name: "alice/app", ID: id, Kind: "orphan"}}},
		Versions: []image.VersionGCPlan{{
			Name: "alice/app",
			Candidates: []image.VersionGCCandidate{{
				Meta: image.ImageMeta{ID: strings.Repeat("b", 64)}, Activity: activity.Revision{Last: now.Add(-40 * expireDay)},
			}},
		}},
	}, now, cutoff)
	listing.Reset()
	if err := mixed.write(&listing); err != nil {
		t.Fatal(err)
	}
	text = listing.String()
	cleanupAt := strings.Index(text, "Image GC cleanup candidates:")
	orphanAt := strings.Index(text, "orphan  alice/app:"+id)
	expiredAt := strings.Index(text, "Expired image versions (last activity before "+formatGCStamp(cutoff)+"):")
	rowAt := strings.Index(text, "expired  alice/app:"+strings.Repeat("b", 64))
	if cleanupAt < 0 || orphanAt < cleanupAt || expiredAt < orphanAt || rowAt < expiredAt || mixed.candidateCount() != 2 {
		t.Fatalf("mixed listing count=%d %q", mixed.candidateCount(), text)
	}
}

func TestImageGCReportSkipsUnreportedApprovals(t *testing.T) {
	const name = "alice/app"
	idA := strings.Repeat("a", 64)
	idB := strings.Repeat("b", 64)
	idC := strings.Repeat("c", 64)
	plan := session.ImageGCPlan{
		Cleanup: image.CleanupPlan{Items: []image.CleanupItem{
			{Name: name, ID: idA, Kind: "pending-cleanup"},
			{Name: name, Kind: "pending-cleanup"},
		}},
		Versions: []image.VersionGCPlan{{
			Name: name, Candidates: []image.VersionGCCandidate{{Meta: image.ImageMeta{ID: idC}}},
		}},
	}
	text := imageGCReportText(t, plan, nil, fmt.Errorf("image gc: cleanup %s:%s is referenced", name, idA))
	for _, want := range []string{
		"skipped " + name + ":" + idA + ": referenced",
		"skipped " + name + ": not executed",
		"skipped " + name + ":" + idC + ": not executed",
		"removed 0 of 3 approved image objects, skipped 3",
	} {
		if !strings.Contains(text, want) || strings.Contains(text, name+"::") {
			t.Fatalf("report missing %q:\n%s", want, text)
		}
	}
	partial := []image.VersionGCOutcome{{Name: name, ID: idA, Action: image.GCActionRemoved}}
	orphanPlan := session.ImageGCPlan{
		Cleanup: image.CleanupPlan{Items: []image.CleanupItem{{Name: name, ID: idA, Kind: "orphan"}}},
		Versions: []image.VersionGCPlan{{
			Name: name, Candidates: []image.VersionGCCandidate{{Meta: image.ImageMeta{ID: idB}}},
		}},
	}
	text = imageGCReportText(t, orphanPlan, partial, errors.New("open repo"))
	if !strings.Contains(text, "removed "+name+":"+idA+"\n") ||
		!strings.Contains(text, "skipped "+name+":"+idB+": not executed (open repo)") ||
		!strings.Contains(text, "removed 1 of 2 approved image objects, skipped 1") {
		t.Fatalf("partial report %q", text)
	}
}

func imageGCReportText(t *testing.T, plan session.ImageGCPlan, outcomes []image.VersionGCOutcome, execErr error) string {
	t.Helper()
	outcomes = imageGCReportedOutcomes(plan, outcomes, execErr)
	var buf bytes.Buffer
	if err := writeImageGCReport(&buf, outcomes, len(outcomes)); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestImagesGCSkipsCleanupReferencedAfterConfirmation(t *testing.T) {
	home, lib, repo, nodes := imageGCFixture(t)
	imagesDir, _ := libFlags(home)
	before := image.VersionManifest{Version: image.VersionManifestVersion, Name: repo.Name(), Images: nodes}
	after := image.VersionManifest{Version: image.VersionManifestVersion, Name: repo.Name()}
	body, err := json.Marshal(map[string]any{"name": repo.Name(), "before": before, "after": after,
		"ids": []string{nodes[0].ID, nodes[1].ID}, "delete_repository": true})
	if err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(imagesDir, ".version-removal.json")
	writeTestFile(t, journal, string(body))
	withTerminalSeam(t, true)
	reader := &confirmReader{answer: "yes\n", mutate: func() {
		rec, err := session.NewVersionRecord("pinned", time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		rec.State = session.StateClosed
		rec.ClosedAt = time.Now().UTC()
		rec.Actions = []session.Action{{
			Seq: 1, Type: session.TypeMount, Image: repo.Name(), Name: repo.Name(),
			FullID: nodes[0].ID, TipID: nodes[0].ID, RuntimeID: "m1",
			Mountpoint: "/mnt/pinned", Mode: session.ModeRO, Status: session.StatusUnmounted,
		}}
		coordinator := session.ReferenceCoordinator{Store: session.Store{Dir: sessionsDir(home)}, ImagesDir: imagesDir}
		if err := coordinator.Apply(nil, &rec); err != nil {
			t.Fatal(err)
		}
	}}
	stdout, stderr, err := runCmdReader(t, reader, imageGCArgs(lib)...)
	if err == nil || !strings.Contains(err.Error(), "is referenced") {
		t.Fatalf("error=%v stderr=%q stdout=%q", err, stderr, stdout)
	}
	if strings.Contains(stdout, "last activity before") || !strings.Contains(stdout, "Image GC cleanup candidates:") ||
		!strings.Contains(stdout, "skipped "+repo.Name()+":"+nodes[0].ID+": referenced") ||
		!strings.Contains(stdout, "removed 0 of 2 approved image objects, skipped 2") {
		t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
	}
	if _, err := os.Stat(journal); err != nil {
		t.Fatalf("refused cleanup removed the journal: %v", err)
	}
}
