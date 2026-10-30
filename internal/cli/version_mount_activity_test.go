package cli

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/activity"
	"github.com/jiangxin/tiler/internal/config"
	"github.com/jiangxin/tiler/internal/image"
	"github.com/jiangxin/tiler/internal/mountinfo"
	"github.com/jiangxin/tiler/internal/session"
)

// mountActivityLine is one decoded image activity record.
type mountActivityLine struct {
	Time          string `json:"time"`
	Event         string `json:"event"`
	Phase         string `json:"phase"`
	Session       string `json:"session"`
	Seq           int    `json:"seq"`
	Mountpoint    string `json:"mountpoint"`
	Image         string `json:"image"`
	Version       string `json:"image_version"`
	SourceVersion string `json:"source_version"`
	SourceEvent   string `json:"source_event"`
	Error         string `json:"error"`
}

// readMountActivity decodes the activity log of one version. A missing
// log reads as no records.
func readMountActivity(t *testing.T, cfg config.Config, name, id string) []mountActivityLine {
	t.Helper()
	dir := filepath.Join(cfg.LogsDir, "images", filepath.FromSlash(name))
	data, err := os.ReadFile(filepath.Join(dir, id+".log"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("read activity log of %s: %v", id, err)
	}
	var lines []mountActivityLine
	for _, text := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		var line mountActivityLine
		if err := json.Unmarshal([]byte(text), &line); err != nil {
			t.Fatalf("activity record %q: %v", text, err)
		}
		lines = append(lines, line)
	}
	return lines
}

// requireMountActivity asserts the exact records one version holds for
// one mount event. An empty source names the tip, which records the mount
// event itself; any other source is the ancestor's dependency record.
func requireMountActivity(t *testing.T, cfg config.Config, name string,
	node image.ImageMeta, source, mountpoint string, want []string,
) {
	t.Helper()
	lines := readMountActivity(t, cfg, name, node.ID)
	if len(lines) != len(want) {
		t.Fatalf("version %s activity = %+v, want one record per phase", node.Description, lines)
	}
	for i, phase := range want {
		line := lines[i]
		if line.Phase != phase || line.Version != node.ID || line.Image != name ||
			line.Mountpoint != mountpoint || line.Session != "dev" || line.Seq != 2 {
			t.Fatalf("version %s line %d = %+v", node.Description, i, line)
		}
		if source == "" {
			if line.Event != activity.EventMount || line.SourceVersion != "" {
				t.Fatalf("tip line %d = %+v, want the mount event itself", i, line)
			}
			continue
		}
		if line.Event != activity.EventImageDependency || line.SourceVersion != source ||
			line.SourceEvent != activity.EventMount {
			t.Fatalf("ancestor %s line %d = %+v, want a dependency on %s",
				node.Description, i, line, source)
		}
	}
}

// requireNoMountActivity asserts one version never received a log.
func requireNoMountActivity(t *testing.T, cfg config.Config, name string, id string) {
	t.Helper()
	if lines := readMountActivity(t, cfg, name, id); len(lines) != 0 {
		t.Fatalf("version %s recorded %+v, want no activity", id, lines)
	}
}

// mountActivityLibrary writes the F, A, B chain of a library, an untouched
// sibling of F, and one version above B that no test mounts. The descendant
// exists so a mount that walked the children of the tip, or the newest head
// of the library, would leave a record behind.
func mountActivityLibrary(t *testing.T, imagesDir string) (base, mid, tip, sibling, descendant image.ImageMeta) {
	t.Helper()
	now := time.Now().UTC()
	base = versionNode("a", image.KindFull, "", now)
	mid = versionNode("b", image.KindIncremental, base.ID, now.Add(time.Minute))
	tip = versionNode("c", image.KindIncremental, mid.ID, now.Add(2*time.Minute))
	sibling = versionNode("d", image.KindIncremental, base.ID, now.Add(3*time.Minute))
	descendant = versionNode("e", image.KindIncremental, tip.ID, now.Add(4*time.Minute))
	writeVersionForest(t, imagesDir, "alice/app", base, mid, tip, sibling, descendant)
	for _, node := range []image.ImageMeta{base, mid, tip} {
		writeVersionPayload(t, imagesDir, "alice/app", node, node.Description)
	}
	return base, mid, tip, sibling, descendant
}

// mountActivityAction records one mount of the given tip.
func mountActivityAction(root, tip string) session.Action {
	return session.Action{Seq: 2, Type: session.TypeMount, Image: "alice/app",
		Name: "alice/app", FullID: root, TipID: tip, RuntimeID: "m2",
		Mountpoint: "/view", Mode: session.ModeRW}
}

// TestVersionMountActivityRecordsTheResolvedStack mounts the tip of an
// F-A-B chain under a library lock the caller already holds. The tip
// records both phases, each ancestor one dependency record per phase,
// and neither the sibling of the root nor the version above the tip
// records anything.
func TestVersionMountActivityRecordsTheResolvedStack(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{ImagesDir: filepath.Join(root, "images"), LogsDir: filepath.Join(root, "logs")}
	base, mid, tip, sibling, descendant := mountActivityLibrary(t, cfg.ImagesDir)
	fake := withMountSeams(t, nil)
	hold, err := image.LockImage(cfg.ImagesDir, "alice/app", image.DefaultLibraryLockTimeout)
	if err != nil {
		t.Fatalf("lock image: %v", err)
	}
	defer hold.Release()
	if _, err := runVersionMountEngine(cfg, "dev", mountActivityAction(base.ID, tip.ID),
		"/view", mountFlags{}, filepath.Join(root, "runtime")); err != nil {
		t.Fatalf("mount: %v", err)
	}
	if len(fake.mountReqs) != 1 {
		t.Fatalf("mount requests = %d, want one", len(fake.mountReqs))
	}
	phases := []string{activity.PhaseStarted, activity.PhaseSucceeded}
	requireMountActivity(t, cfg, "alice/app", tip, "", "/view", phases)
	requireMountActivity(t, cfg, "alice/app", mid, tip.ID, "/view", phases)
	requireMountActivity(t, cfg, "alice/app", base, tip.ID, "/view", phases)
	requireNoMountActivity(t, cfg, "alice/app", sibling.ID)
	requireNoMountActivity(t, cfg, "alice/app", descendant.ID)
	repo, err := image.OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range []image.ImageMeta{base, mid, tip} {
		object, err := repo.ObjectDirPath(node.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(object, activity.LogName)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("object directory of %s has %s: %v", node.Description, activity.LogName, err)
		}
	}
}

// TestVersionMountActivityRecordsAFailedMount keeps the attempt and its
// failure in the logs when the engine refuses to mount.
func TestVersionMountActivityRecordsAFailedMount(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{ImagesDir: filepath.Join(root, "images"), LogsDir: filepath.Join(root, "logs")}
	base, mid, tip, sibling, descendant := mountActivityLibrary(t, cfg.ImagesDir)
	fake := withMountSeams(t, nil)
	fake.mountErr = errors.New("fuse refused")
	_, err := runVersionMountEngine(cfg, "dev", mountActivityAction(base.ID, tip.ID),
		"/view", mountFlags{}, filepath.Join(root, "runtime"))
	if err == nil || !strings.Contains(err.Error(), "fuse refused") {
		t.Fatalf("mount err = %v, want the engine failure", err)
	}
	for _, node := range []image.ImageMeta{base, mid, tip} {
		lines := readMountActivity(t, cfg, "alice/app", node.ID)
		if len(lines) != 2 || lines[0].Phase != activity.PhaseStarted ||
			lines[1].Phase != activity.PhaseFailed || lines[1].Error != "fuse refused" {
			t.Fatalf("version %s activity = %+v, want the started and failed phases",
				node.Description, lines)
		}
	}
	requireNoMountActivity(t, cfg, "alice/app", sibling.ID)
	requireNoMountActivity(t, cfg, "alice/app", descendant.ID)
}

// TestSessionReplayActivityFollowsTheRecordedTip replays a record pinned
// to a historical tip: the replayed stack records the activity and the
// versions above it stay untouched.
func TestSessionReplayActivityFollowsTheRecordedTip(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{ImagesDir: filepath.Join(root, "images"), LogsDir: filepath.Join(root, "logs")}
	base, mid, tip, sibling, descendant := mountActivityLibrary(t, cfg.ImagesDir)
	withMountSeams(t, nil)
	hold, err := image.LockImage(cfg.ImagesDir, "alice/app", image.DefaultLibraryLockTimeout)
	if err != nil {
		t.Fatalf("lock image: %v", err)
	}
	defer hold.Release()
	action := mountActivityAction(base.ID, mid.ID)
	if _, err := sessionMount(cfg, "dev", io.Discard)(action); err != nil {
		t.Fatalf("replay: %v", err)
	}
	phases := []string{activity.PhaseStarted, activity.PhaseSucceeded}
	requireMountActivity(t, cfg, "alice/app", mid, "", "/view", phases)
	requireMountActivity(t, cfg, "alice/app", base, mid.ID, "/view", phases)
	requireNoMountActivity(t, cfg, "alice/app", tip.ID)
	requireNoMountActivity(t, cfg, "alice/app", sibling.ID)
	requireNoMountActivity(t, cfg, "alice/app", descendant.ID)
}

// TestSessionReplayMountsDesiredStackWhenServedDiffers covers a rebased
// record whose served member list is not the desired one. The replay
// request mounts the desired stack and asks the engine to prune the
// reused upper against those lowers.
func TestSessionReplayMountsDesiredStackWhenServedDiffers(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{ImagesDir: filepath.Join(root, "images"), LogsDir: filepath.Join(root, "logs")}
	base, _, servedTip, desiredTip, _ := mountActivityLibrary(t, cfg.ImagesDir)
	writeVersionPayload(t, cfg.ImagesDir, "alice/app", desiredTip, desiredTip.Description)
	fake := withMountSeams(t, nil)
	hold, err := image.LockImage(cfg.ImagesDir, "alice/app", image.DefaultLibraryLockTimeout)
	if err != nil {
		t.Fatalf("lock image: %v", err)
	}
	defer hold.Release()
	action := mountActivityAction(base.ID, desiredTip.ID)
	action.ServedID, action.ServedTip = base.ID, servedTip.ID
	if _, err := sessionMount(cfg, "dev", io.Discard)(action); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(fake.mountReqs) != 1 {
		t.Fatalf("mount requests = %d, want one", len(fake.mountReqs))
	}
	req := fake.mountReqs[0]
	if !req.PruneUpper {
		t.Fatal("PruneUpper = false, want the changed lower stack to prune")
	}
	if len(req.Members) != 2 ||
		!strings.Contains(req.Members[0].FSName, base.ID) ||
		!strings.Contains(req.Members[1].FSName, desiredTip.ID) {
		t.Fatalf("members = %#v, want the desired stack", req.Members)
	}
	for _, member := range req.Members {
		if strings.Contains(member.FSName, servedTip.ID) {
			t.Fatalf("member = %#v, want the desired stack, not the served tip", member)
		}
	}
}

// TestHealthyOpenRenewsTheMountActivity records the stack of a view the
// replay adopts instead of remounting, through the production wiring.
func TestHealthyOpenRenewsTheMountActivity(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{ImagesDir: filepath.Join(root, "images"),
		LogsDir: filepath.Join(root, "logs"), SessionsDir: filepath.Join(root, "sessions")}
	base, mid, tip, sibling, descendant := mountActivityLibrary(t, cfg.ImagesDir)
	writeVersionSession(t, cfg.SessionsDir, "dev", "alice/app", base.ID, tip.ID)
	engine := sessionEngine(cfg)
	engine.Health = func(string) bool { return true }
	table := func() ([]mountinfo.Mount, error) { return nil, nil }
	engine.LoadTable = table
	called := false
	err := engine.Open("dev", func(session.Action) (session.Result, error) {
		called = true
		return session.Result{}, nil
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if called {
		t.Fatal("open remounted a healthy view")
	}
	// writeVersionSession records seq 1 at /workspace.
	lines := readMountActivity(t, cfg, "alice/app", tip.ID)
	if len(lines) != 1 || lines[0].Event != activity.EventMount ||
		lines[0].Phase != activity.PhaseSucceeded || lines[0].Seq != 1 ||
		lines[0].Mountpoint != "/workspace" || lines[0].Session != "dev" {
		t.Fatalf("tip activity = %+v, want the renewed mount", lines)
	}
	for _, node := range []image.ImageMeta{mid, base} {
		lines := readMountActivity(t, cfg, "alice/app", node.ID)
		if len(lines) != 1 || lines[0].Event != activity.EventImageDependency ||
			lines[0].SourceVersion != tip.ID || lines[0].SourceEvent != activity.EventMount ||
			lines[0].Phase != activity.PhaseSucceeded {
			t.Fatalf("version %s activity = %+v, want the renewed dependency",
				node.Description, lines)
		}
	}
	requireNoMountActivity(t, cfg, "alice/app", sibling.ID)
	requireNoMountActivity(t, cfg, "alice/app", descendant.ID)
}

// TestHealthyOpenRecordsServedStackWhenDesiredDiffers keeps the activity
// of an adopted view on the stack that view still serves. The desired
// pin names a different lower stack, and a healthy open does not remount
// it, so that desired tip stays out of the image logs.
func TestHealthyOpenRecordsServedStackWhenDesiredDiffers(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{ImagesDir: filepath.Join(root, "images"),
		LogsDir: filepath.Join(root, "logs"), SessionsDir: filepath.Join(root, "sessions")}
	base, mid, servedTip, desiredTip, descendant := mountActivityLibrary(t, cfg.ImagesDir)
	rec, err := session.NewVersionRecord("dev", time.Now())
	if err != nil {
		t.Fatalf("new record: %v", err)
	}
	action := mountActivityAction(base.ID, desiredTip.ID)
	action.ServedID, action.ServedTip = base.ID, servedTip.ID
	action.Status = session.StatusMounted
	action.Timestamp = time.Now()
	rec.Actions = []session.Action{action}
	if err := (session.Store{Dir: cfg.SessionsDir}).SaveVersion(rec); err != nil {
		t.Fatalf("save record: %v", err)
	}
	engine := sessionEngine(cfg)
	engine.Health = func(string) bool { return true }
	engine.LoadTable = func() ([]mountinfo.Mount, error) { return nil, nil }
	called := false
	err = engine.Open("dev", func(session.Action) (session.Result, error) {
		called = true
		return session.Result{}, nil
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if called {
		t.Fatal("open remounted a healthy view")
	}
	phases := []string{activity.PhaseSucceeded}
	requireMountActivity(t, cfg, "alice/app", servedTip, "", "/view", phases)
	requireMountActivity(t, cfg, "alice/app", mid, servedTip.ID, "/view", phases)
	requireMountActivity(t, cfg, "alice/app", base, servedTip.ID, "/view", phases)
	requireNoMountActivity(t, cfg, "alice/app", desiredTip.ID)
	requireNoMountActivity(t, cfg, "alice/app", descendant.ID)
}

// TestVersionMountActivityReportsALostOutcomeRecord mounts a view and then
// loses its outcome record: the started phase lands, the view serves, and the
// command layer reports the lost record as session.ErrActivityFailed instead
// of a mount failure, so the session layer keeps a mounted action.
func TestVersionMountActivityReportsALostOutcomeRecord(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{ImagesDir: filepath.Join(root, "images"), LogsDir: filepath.Join(root, "logs")}
	base, mid, tip, _, _ := mountActivityLibrary(t, cfg.ImagesDir)
	repo, err := image.OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	dir, err := repo.ObjectDirPath(tip.ID)
	if err != nil {
		t.Fatalf("object dir of %s: %v", tip.ID, err)
	}
	fake := withMountSeams(t, nil)
	// The started phase created the object directory. Replacing it with a
	// file lets the mount serve while the outcome batch can no longer
	// address the tip, which is the one failure the sentinel exists for.
	fake.mountHook = func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Fatalf("drop object dir: %v", err)
		}
		if err := os.WriteFile(dir, []byte("blocked"), 0o644); err != nil {
			t.Fatalf("block object dir: %v", err)
		}
	}
	result, err := runVersionMountEngine(cfg, "dev", mountActivityAction(base.ID, tip.ID),
		"/view", mountFlags{}, filepath.Join(root, "runtime"))
	if !errors.Is(err, session.ErrActivityFailed) {
		t.Fatalf("mount err = %v, want the lost-record sentinel", err)
	}
	if result.Mountpoint != "/view" {
		t.Fatalf("result = %+v, want the served view", result)
	}
	// The outcome batch prepares the whole chain before its first write,
	// so the blocked tip leaves the ancestors with the started phase only.
	for _, node := range []image.ImageMeta{mid, base} {
		requireMountActivity(t, cfg, "alice/app", node, tip.ID, "/view",
			[]string{activity.PhaseStarted})
	}
}

// TestHealthyOpenSkipsFileMounts keeps an external file view, which holds
// no library pin, out of the image logs.
func TestHealthyOpenSkipsFileMounts(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{ImagesDir: filepath.Join(root, "images"),
		LogsDir: filepath.Join(root, "logs"), SessionsDir: filepath.Join(root, "sessions")}
	rec, err := session.NewVersionRecord("dev", time.Now())
	if err != nil {
		t.Fatalf("new record: %v", err)
	}
	rec.Actions = []session.Action{{Seq: 1, Type: session.TypeMount,
		Image: "/images/vendor.sqsh", Mountpoint: "/workspace",
		TipID: strings.Repeat("ab", 32), RuntimeID: "m1", Mode: session.ModeRW,
		Status: session.StatusMounted, Timestamp: time.Now()}}
	if err := (session.Store{Dir: cfg.SessionsDir}).SaveVersion(rec); err != nil {
		t.Fatalf("save record: %v", err)
	}
	engine := sessionEngine(cfg)
	engine.Health = func(string) bool { return true }
	engine.LoadTable = func() ([]mountinfo.Mount, error) { return nil, nil }
	if err := engine.Open("dev", func(session.Action) (session.Result, error) {
		t.Fatal("open remounted a healthy file view")
		return session.Result{}, nil
	}); err != nil {
		t.Fatalf("open: %v", err)
	}
	err = filepath.WalkDir(cfg.ImagesDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Name() == activity.LogName {
			t.Fatalf("file view recorded %s", path)
		}
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("walk library: %v", err)
	}
}
