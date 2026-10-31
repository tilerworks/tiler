package cli

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jiangxin/tiler/internal/config"
	"github.com/jiangxin/tiler/internal/container"
	"github.com/jiangxin/tiler/internal/image"
	"github.com/jiangxin/tiler/internal/session"
	"github.com/jiangxin/tiler/internal/workspace"
)

// stackPin is one recorded pin (full id, tip id) the fake stack
// resolver of the replay-prune tests answers.
type stackPin struct {
	fullID string
	tipID  string
}

// fakeStackResolver replaces resolveStackTip with a table-driven
// resolver: every pin maps to the ordered member ids of one
// single-file stack of the given format, materialized as fake
// container files in a temporary directory so the replay's format
// checks pass. A pin the table lacks fails the way a vanished hold
// leaf fails the real resolver. It returns the pins the resolver was
// asked for, in call order.
func fakeStackResolver(t *testing.T, format string, stacks map[stackPin][]string) *[]stackPin {
	t.Helper()
	dir := t.TempDir()
	original := resolveStackTip
	t.Cleanup(func() { resolveStackTip = original })
	calls := &[]stackPin{}
	resolveStackTip = func(_ config.Config, name, fullID, tipID string) (image.Target, error) {
		pin := stackPin{fullID, tipID}
		*calls = append(*calls, pin)
		ids, ok := stacks[pin]
		if !ok {
			return image.Target{}, fmt.Errorf(
				"mount: %s id %s: unknown stack tip of image %s", name, tipID, fullID)
		}
		members := make([]image.Member, 0, len(ids))
		for _, id := range ids {
			path := filepath.Join(dir, id+image.SquashExt)
			writeTestFile(t, path, string(containerBody(format, id)))
			members = append(members, image.Member{Path: path, ID: id})
		}
		return image.Target{
			Library: true,
			Name:    name,
			ID:      ids[0],
			Tip:     ids[len(ids)-1],
			Format:  format,
			Members: members,
		}, nil
	}
	return calls
}

// writePruneRecord seeds the "dev" record with one library mount
// action carrying the given desired and served pins.
func writePruneRecord(
	t *testing.T,
	home, work, fullID, tipID, servedID, servedTip string,
) {
	t.Helper()
	action := libraryAction("alice/app", filepath.Join(work, "view"), session.ModeRW)
	action.FullID, action.TipID = fullID, tipID
	action.ServedID, action.ServedTip = servedID, servedTip
	writeSessionRecord(t, sessionsDir(home), "dev", action)
}

// openPruneSession replays the "dev" record and returns the one
// captured mount request with the command's stderr.
func openPruneSession(t *testing.T, lib []string, f *mountFake) (workspace.Request, string) {
	t.Helper()
	_, stderr, _, err := runCmd(t, append(lib, "session", "open", "dev")...)
	if err != nil {
		t.Fatalf("session open: %v\n%s", err, stderr)
	}
	if len(f.mountReqs) != 1 {
		t.Fatalf("engine calls = %d, want 1", len(f.mountReqs))
	}
	return f.mountReqs[0], stderr
}

// TestSessionReplayPrunesUpperWhenLowerStackChanged covers the
// trigger: a rebased record names the served stack its live view
// froze at, that stack resolves to other members than the desired
// stack, so the lower layers changed and the replay request must ask
// the engine to prune the reused upper.
func TestSessionReplayPrunesUpperWhenLowerStackChanged(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	f := withMountSeams(t, nil)
	pinPortableHealth(t, f)
	calls := fakeStackResolver(t, image.FormatSquashfs, map[stackPin][]string{
		{"full-a", "tip-b"}:  {"full-a", "tip-b"},
		{"full-a", "full-a"}: {"full-a"},
	})
	writePruneRecord(t, home, work, "full-a", "tip-b", "full-a", "full-a")

	req, stderr := openPruneSession(t, lib, f)
	if !req.PruneUpper {
		t.Fatal("PruneUpper = false, want the changed lower stack to prune")
	}
	if stderr != "" {
		t.Fatalf("replay warned: %q", stderr)
	}
	// Both pins resolved: the desired stack for the mount and the
	// served stack for the comparison.
	if len(*calls) != 2 {
		t.Fatalf("resolver calls = %v, want the desired and the served pin", *calls)
	}
}

// TestSessionReplayKeepsUpperWhenServedStackMatches covers the
// negative side of the comparison: two pins spelled differently but
// resolving to the same ordered member ids are the same lower stack,
// so the reused upper stays untouched.
func TestSessionReplayKeepsUpperWhenServedStackMatches(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	f := withMountSeams(t, nil)
	pinPortableHealth(t, f)
	// The served pin carries no tip, the pre-field spelling whose
	// resolution expands to the leaf head stack — here the very
	// stack the desired pin names.
	calls := fakeStackResolver(t, image.FormatSquashfs, map[stackPin][]string{
		{"full-a", "tip-b"}: {"full-a", "tip-b"},
		{"full-a", ""}:      {"full-a", "tip-b"},
	})
	writePruneRecord(t, home, work, "full-a", "tip-b", "full-a", "")

	req, stderr := openPruneSession(t, lib, f)
	if req.PruneUpper {
		t.Fatal("PruneUpper = true, want an unchanged lower stack to keep the upper")
	}
	if stderr != "" {
		t.Fatalf("replay warned: %q", stderr)
	}
	if len(*calls) != 2 {
		t.Fatalf("resolver calls = %v, want both pins resolved", *calls)
	}
}

// TestSessionReplayOldRecordNeverPrunes pins the compatibility rule:
// a record without served ids — written before the fields existed or
// simply never rebased — resolves its desired stack alone and keeps
// the upper untouched.
func TestSessionReplayOldRecordNeverPrunes(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	f := withMountSeams(t, nil)
	pinPortableHealth(t, f)
	calls := fakeStackResolver(t, image.FormatSquashfs, map[stackPin][]string{
		{"full-a", "tip-b"}: {"full-a", "tip-b"},
	})
	writePruneRecord(t, home, work, "full-a", "tip-b", "", "")

	req, stderr := openPruneSession(t, lib, f)
	if req.PruneUpper {
		t.Fatal("PruneUpper = true, want a record without served ids to keep the upper")
	}
	if stderr != "" {
		t.Fatalf("replay warned: %q", stderr)
	}
	if len(*calls) != 1 || (*calls)[0] != (stackPin{"full-a", "tip-b"}) {
		t.Fatalf("resolver calls = %v, want only the desired pin", *calls)
	}
}

// TestSessionReplayDMGActionNeverPrunes covers the dmg side: a dmg
// view is never rebased, so its recorded action carries no served
// ids and the replay resolves no served stack and asks for no prune
// — a dmg attach has no upper directory to prune in the first place.
func TestSessionReplayDMGActionNeverPrunes(t *testing.T) {
	if _, err := container.Lookup(image.FormatDMG); err != nil {
		t.Skip("dmg is not registered on this OS")
	}
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	f := withMountSeams(t, nil)
	pinPortableHealth(t, f)
	calls := fakeStackResolver(t, image.FormatDMG, map[stackPin][]string{
		{"full-a", "full-a"}: {"full-a"},
	})
	writePruneRecord(t, home, work, "full-a", "full-a", "", "")

	req, stderr := openPruneSession(t, lib, f)
	if req.PruneUpper {
		t.Fatal("PruneUpper = true, want a dmg action to keep the flag clear")
	}
	if stderr != "" {
		t.Fatalf("replay warned: %q", stderr)
	}
	if len(*calls) != 1 {
		t.Fatalf("resolver calls = %v, want only the desired pin", *calls)
	}
}

// TestSessionReplayUnresolvableServedStackWarnsAndKeepsUpper pins the
// conservative failure path: a served stack whose leaf no longer
// resolves cannot prove a lower change, so the replay warns, keeps
// the upper unpruned, and still mounts the desired stack — pruning is
// an optimization and never fails a recovery.
func TestSessionReplayUnresolvableServedStackWarnsAndKeepsUpper(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	f := withMountSeams(t, nil)
	pinPortableHealth(t, f)
	// The served pin has no table entry, the shape of a hold leaf
	// that vanished behind the record's back.
	fakeStackResolver(t, image.FormatSquashfs, map[stackPin][]string{
		{"full-a", "tip-b"}: {"full-a", "tip-b"},
	})
	writePruneRecord(t, home, work, "full-a", "tip-b", "gone-full", "gone-full")

	req, stderr := openPruneSession(t, lib, f)
	if req.PruneUpper {
		t.Fatal("PruneUpper = true, want an unresolvable served stack to skip the prune")
	}
	if !strings.Contains(stderr, "warning:") ||
		!strings.Contains(stderr, "the served stack gone-full tip gone-full no longer resolves") {
		t.Fatalf("stderr = %q, want a prune warning quoting the full served pin", stderr)
	}
}

// TestSessionReplayUnresolvableTiplessServedStackWarnsCleanly pins the
// warning rendering of the tip-less served spelling: a pin recorded
// as (full id, "") quotes the id alone, with no dangling "tip" word
// and no empty half in the message.
func TestSessionReplayUnresolvableTiplessServedStackWarnsCleanly(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	f := withMountSeams(t, nil)
	pinPortableHealth(t, f)
	// The served pin uses the tip-less recorded spelling and has no
	// table entry, the shape of a vanished full-id-only hold.
	fakeStackResolver(t, image.FormatSquashfs, map[stackPin][]string{
		{"full-a", "tip-b"}: {"full-a", "tip-b"},
	})
	writePruneRecord(t, home, work, "full-a", "tip-b", "gone-full", "")

	req, stderr := openPruneSession(t, lib, f)
	if req.PruneUpper {
		t.Fatal("PruneUpper = true, want an unresolvable served stack to skip the prune")
	}
	if !strings.Contains(stderr, "the served stack gone-full no longer resolves") ||
		strings.Contains(stderr, "gone-full tip") {
		t.Fatalf("stderr = %q, want the warning to quote the tip-less pin without a"+
			" dangling tip segment", stderr)
	}
}

// TestRunEnginePruneIncompleteFollowsTheWindow pins the reduction
// from a workspace window onto the session result. PruneFailed is
// set only for a non-empty upper, so a finished window and an empty
// upper both leave the flag clear and the replay drops the served
// pins. A failure reported without a prune request does not count.
func TestRunEnginePruneIncompleteFollowsTheWindow(t *testing.T) {
	orig := mountEngine
	t.Cleanup(func() { mountEngine = orig })
	tests := []struct {
		name   string
		prune  bool
		rebase bool
		failed bool
		want   bool
	}{
		{name: "failed window", prune: true, failed: true, want: true},
		{name: "finished or empty upper", prune: true, failed: false, want: false},
		{name: "prune not requested", prune: false, failed: true, want: false},
		{name: "reconciled upper with failed optimization", prune: true, rebase: true, failed: true, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mountEngine = func(req workspace.Request) (*workspace.Workspace, error) {
				return &workspace.Workspace{
					MergedDir:   req.MergedDir,
					PruneFailed: tc.failed,
				}, nil
			}
			req := workspace.Request{PruneUpper: tc.prune, MergedDir: "/view"}
			if tc.rebase {
				req.Rebase = &workspace.UpperRebase{}
			}
			got, err := runEngine(req)
			if err != nil {
				t.Fatal(err)
			}
			if got.Mountpoint != "/view" {
				t.Fatalf("mountpoint = %q, want the view to stay mounted", got.Mountpoint)
			}
			if got.PruneIncomplete != tc.want {
				t.Fatalf("PruneIncomplete = %v, want %v", got.PruneIncomplete, tc.want)
			}
		})
	}
}

// TestSessionReplayServedPinsFollowPruneWindow carries a window
// failure into the record. A failed window still opens the session
// and leaves served_id and served_tip in place. A finished window,
// an empty upper, and a replay that did not ask to prune all clear
// those pins. The warning and the omitted done line belong to the
// workspace window.
func TestSessionReplayServedPinsFollowPruneWindow(t *testing.T) {
	differ := map[stackPin][]string{
		{"full-a", "tip-b"}:  {"full-a", "tip-b"},
		{"full-a", "full-a"}: {"full-a"},
	}
	same := map[stackPin][]string{
		{"full-a", "tip-b"}: {"full-a", "tip-b"},
	}
	tests := []struct {
		name      string
		servedTip string
		stacks    map[stackPin][]string
		failed    bool
		reused    bool
		prune     bool
		keep      bool
	}{
		{name: "failed window", servedTip: "full-a", stacks: differ, failed: true, prune: true, keep: true},
		{name: "finished or empty upper", servedTip: "full-a", stacks: differ, prune: true, keep: false},
		{name: "prune not requested", servedTip: "tip-b", stacks: same, failed: true, keep: false},
		{name: "reused view", servedTip: "full-a", stacks: differ, reused: true, prune: true, keep: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home, work := withWorkspace(t)
			_, lib := libFlags(home)
			f := withMountSeams(t, nil)
			f.pruneFailed = tc.failed
			f.reused = tc.reused
			pinPortableHealth(t, f)
			fakeStackResolver(t, image.FormatSquashfs, tc.stacks)
			writePruneRecord(t, home, work, "full-a", "tip-b", "full-a", tc.servedTip)

			stdout, stderr, _, err := runCmd(t, append(lib, "session", "open", "dev")...)
			if err != nil {
				t.Fatalf("session open: %v\n%s", err, stderr)
			}
			if stdout != "opened dev\n" {
				t.Fatalf("stdout = %q, want the session to open", stdout)
			}
			if strings.Contains(stderr, "pruning upper ... done") {
				t.Fatalf("stderr = %q, want no done line", stderr)
			}
			if len(f.mountReqs) != 1 || f.mountReqs[0].PruneUpper != tc.prune {
				t.Fatalf("PruneUpper = %v, want %v", f.mountReqs[0].PruneUpper, tc.prune)
			}
			action := loadSessionRecord(t, sessionsDir(home), "dev").Actions[0]
			if action.Status != session.StatusMounted {
				t.Fatalf("action = %+v, want mounted", action)
			}
			kept := action.ServedID == "full-a" && action.ServedTip == tc.servedTip
			if kept != tc.keep {
				t.Fatalf("served pins = (%q, %q), keep = %v", action.ServedID, action.ServedTip, tc.keep)
			}
		})
	}
}
