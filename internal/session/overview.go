package session

import (
	"errors"

	"github.com/jiangxin/tiler/internal/mountinfo"
)

// Health annotations of a live mount action in the overview.
const (
	// HealthHealthy marks a view that passes the shared health check.
	HealthHealthy = "healthy"
	// HealthStale marks a view that fails it; open restores the view.
	HealthStale = "stale"
)

// Overview is the aggregate read model of the status command: every
// record reconciled against one mount table snapshot, plus the tiler
// views that stay mounted without an active record claiming them.
type Overview struct {
	// Current carries the environment session marker verbatim; it
	// may be empty and may match no record, which is not an error.
	Current string
	// Sessions is one view per record, ordered by file name.
	Sessions []View
	// Orphans lists the mounted views no readable active record
	// claims.
	Orphans []Orphan
}

// View is one record in the overview.
type View struct {
	// Summary is the one-line read model of the record.
	Summary
	// Current marks the session named by the environment.
	Current bool
	// Locked marks a record another operation held at scan time;
	// its contents stay unreported and its actions unexpanded until
	// the operation finishes.
	Locked bool
	// Actions are the record's actions with mount health
	// annotations. Only active records expand their actions.
	Actions []ActionView
}

// ActionView is one recorded action with the health of its view.
type ActionView struct {
	// Action is the recorded action.
	Action
	// Health is healthy or stale for a live mount action and empty
	// for every other action.
	Health string
}

// Orphan is one mounted tiler view combination that no active record
// claims.
type Orphan struct {
	// Point is the mountpoint serving the view.
	Point string
	// FSType is the mount table's filesystem type of the point.
	FSType string
	// RuntimeDir is the runtime combination directory the view
	// belongs to.
	RuntimeDir string
}

// Overview reconciles every record with one mount table snapshot.
// Records are read under the non-blocking lock and a busy one is
// marked locked without expanding its actions. Orphan detection
// needs the configured sessions root: an empty SessionsDir skips it.
func (e *Engine) Overview(current string) (Overview, error) {
	if e.useDiskImages() {
		return e.overviewFromDisk(current)
	}
	names, err := e.names()
	if err != nil {
		return Overview{}, err
	}
	table, err := e.loadTable()
	if err != nil {
		return Overview{}, err
	}
	health := e.healthFor(table)
	overview := Overview{Current: current}
	claimed := map[string]bool{}
	for _, name := range names {
		view, err := e.sessionView(name, current, health)
		if err != nil {
			return Overview{}, err
		}
		if view == nil {
			continue
		}
		collectClaimed(claimed, *view)
		overview.Sessions = append(overview.Sessions, *view)
	}
	overview.Orphans = e.orphanViews(table, claimed)
	return overview, nil
}

// sessionView reads one record for the overview. A record another
// operation holds — or whose lock cannot be read — is reported by
// name and marked locked; the record contents stay unreported until
// the operation finishes. A record removed after the listing drops
// out, and a corrupt record is reported, never silently dropped.
func (e *Engine) sessionView(name, current string, health HealthFunc) (*View, error) {
	release, err := e.lockFor(name, 0)
	switch {
	case err == nil:
		defer release()
	case errors.Is(err, ErrLockUnavailable):
		// No advisory locking on this platform: the atomic record
		// write keeps the unlocked read safe.
	default:
		return &View{Summary: Summary{Name: name}, Locked: true}, nil
	}
	rec, err := e.Load(name)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	view := &View{
		Summary: Summarize(rec),
		Current: current != "" && rec.Name == current,
	}
	if rec.State == StateActive {
		view.Actions = annotateActions(rec.Actions, health)
	}
	return view, nil
}

// annotateActions attaches the health mark to every live mount
// action. A torn-down mount and every umount action has no view to
// check and stays unannotated.
func annotateActions(actions []Action, health HealthFunc) []ActionView {
	views := make([]ActionView, 0, len(actions))
	for _, action := range actions {
		view := ActionView{Action: action}
		if action.Type == TypeMount && action.Status != StatusUnmounted &&
			action.Mountpoint != "" {
			view.Health = HealthStale
			if health(action.Mountpoint) {
				view.Health = HealthHealthy
			}
		}
		views = append(views, view)
	}
	return views
}

// collectClaimed adds the live mountpoints of one active record to
// the claim set the orphan detection reconciles against. A locked
// record exposes no actions, so its views stay unclaimed while its
// lock is held.
func collectClaimed(claimed map[string]bool, view View) {
	if view.State != StateActive {
		return
	}
	for _, action := range view.Actions {
		if action.Type == TypeMount && action.Status != StatusUnmounted &&
			action.Mountpoint != "" {
			claimed[action.Mountpoint] = true
		}
	}
}

// orphanViews lists the mounted tiler view combinations no readable
// active record claims. Only entries whose runtime combination
// directory lives under the configured sessions root are candidates,
// so status never guesses tiler ownership of foreign mounts; a
// read-only direct mount without a runtime trace never lists.
func (e *Engine) orphanViews(table []mountinfo.Mount, claimed map[string]bool) []Orphan {
	if e.SessionsDir == "" {
		return nil
	}
	var order []string
	groups := map[string][]mountinfo.Mount{}
	for _, mount := range table {
		runtimeDir := viewRuntimeDir(mount)
		if runtimeDir == "" || !underPrefix(runtimeDir, e.SessionsDir) {
			continue
		}
		if _, ok := groups[runtimeDir]; !ok {
			order = append(order, runtimeDir)
		}
		groups[runtimeDir] = append(groups[runtimeDir], mount)
	}
	var orphans []Orphan
	for _, runtimeDir := range order {
		entries := groups[runtimeDir]
		if claimsAny(entries, claimed) {
			continue
		}
		orphans = append(orphans, orphanOf(entries, e.SessionsDir, runtimeDir))
	}
	return orphans
}

// claimsAny reports whether one recorded mountpoint matches any
// entry of the combination.
func claimsAny(entries []mountinfo.Mount, claimed map[string]bool) bool {
	for _, entry := range entries {
		if claimed[entry.Point] {
			return true
		}
	}
	return false
}

// orphanOf picks the combination's representative: the entry served
// outside the runtime tree — the user-facing merged view — when one
// exists, and the first entry otherwise.
func orphanOf(entries []mountinfo.Mount, runtimeRoot, runtimeDir string) Orphan {
	representative := entries[0]
	for _, entry := range entries {
		if !underPrefix(entry.Point, runtimeRoot) {
			representative = entry
			break
		}
	}
	return Orphan{
		Point:      representative.Point,
		FSType:     representative.FSType,
		RuntimeDir: runtimeDir,
	}
}
