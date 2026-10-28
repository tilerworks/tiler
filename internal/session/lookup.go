package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jiangxin/tiler/internal/mountinfo"
)

// PathSession is one reverse-lookup hit: the session whose recorded
// view covers the queried path.
type PathSession struct {
	// Path is the normalized form of the queried path.
	Path string
	// Record is the covering session's record.
	Record Record
	// Mountpoint is the recorded view point covering the path.
	Mountpoint string
	// Stale reports that the covering view failed the shared health
	// check: the record is present but the mount is dead or its
	// table entry vanished. Open restores the view.
	Stale bool
}

// SessionForPath locates the session whose recorded view covers one
// path, typically a deep path inside a merged view. The chain is:
// normalize the path, match the deepest covering view in the mount
// table, and find the record that mounted it. The table lookup is
// pure, so a dead mount still resolves. When the table entry
// vanished, the scan of active records falls back to the recorded
// mountpoints and the shared health check reports the stale shape.
// A path inside the runtime tree — an upper, a work directory, or a
// lower transit point — resolves back through the combination that
// owns its runtime directory. A path no session covers reports a
// clear error. The hit carries the normalized query path.
func (e *Engine) SessionForPath(path string) (PathSession, error) {
	normalized, err := normalizeForLookup(path)
	if err != nil {
		return PathSession{}, err
	}
	if e.useDiskImages() {
		return e.resolveDiskPath(path, normalized)
	}
	hit, err := e.resolveNormalizedPath(path, normalized)
	if err != nil {
		return PathSession{}, err
	}
	hit.Path = normalized
	return hit, nil
}

// resolveNormalizedPath walks the lookup chain for one normalized
// path against a fresh table snapshot.
func (e *Engine) resolveNormalizedPath(original, normalized string) (PathSession, error) {
	table, err := e.loadTable()
	if err != nil {
		return PathSession{}, err
	}
	health := e.healthFor(table)
	if e.SessionsDir != "" && underPrefix(normalized, e.SessionsDir) &&
		hasRuntimeSegment(e.SessionsDir, normalized) {
		return e.sessionForRuntimePath(normalized, table, health)
	}
	if view, ok := mountinfo.LongestView(table, normalized); ok {
		return e.sessionForMountpoint(view.Point, health)
	}
	return e.staleSessionForPath(original, normalized, health)
}

// sessionForMountpoint matches one exact mount point against the
// active records and classifies the covering view's health.
func (e *Engine) sessionForMountpoint(
	point string,
	health HealthFunc,
) (PathSession, error) {
	rec, found, err := e.recordForMountpoint(point)
	if err != nil {
		return PathSession{}, err
	}
	if !found {
		return PathSession{}, fmt.Errorf(
			"session: no session covers the mount at %s", point)
	}
	return PathSession{Record: rec, Mountpoint: point, Stale: !health(point)}, nil
}

// staleSessionForPath is the fallback for a vanished table entry:
// the deepest recorded mountpoint of an active session that covers
// the literal path wins, and the shared health check reports the
// stale shape.
func (e *Engine) staleSessionForPath(original, normalized string, health HealthFunc) (PathSession, error) {
	var (
		best  PathSession
		found bool
	)
	err := e.eachRecord(func(rec Record) bool {
		if rec.State != StateActive {
			return false
		}
		for _, action := range rec.Actions {
			if !covers(action, normalized) {
				continue
			}
			if !found || len(action.Mountpoint) > len(best.Mountpoint) {
				best = PathSession{
					Record:     rec,
					Mountpoint: action.Mountpoint,
					Stale:      !health(action.Mountpoint),
				}
				found = true
			}
		}
		return false
	})
	if err != nil {
		return PathSession{}, err
	}
	if !found {
		return PathSession{}, fmt.Errorf("session: no session covers %s", original)
	}
	return best, nil
}

// covers reports whether one action records a live mount whose
// point is the path or a directory above it.
func covers(action Action, path string) bool {
	return action.Type == TypeMount &&
		action.Status != StatusUnmounted &&
		action.Mountpoint != "" &&
		underPrefix(path, action.Mountpoint)
}

// sessionForRuntimePath resolves a path inside a session's runtime
// subtree back to the owning session: the path itself names the
// session (<SessionsDir>/<name>/runtime/...), the mount table links
// a live view to its combination directory, and when that entry
// vanished the recorded RuntimeID confirms the combination.
func (e *Engine) sessionForRuntimePath(
	path string,
	table []mountinfo.Mount,
	health HealthFunc,
) (PathSession, error) {
	name, runtimeDir, ok := sessionRuntimeOf(e.SessionsDir, path)
	if !ok {
		return PathSession{}, fmt.Errorf("session: no session covers %s", path)
	}
	for _, mount := range table {
		if viewRuntimeDir(mount) == runtimeDir {
			return e.sessionForMountpoint(mount.Point, health)
		}
	}
	return e.staleRuntimeSession(name, runtimeDir, health)
}

// staleRuntimeSession loads the session named by the runtime path
// and confirms one of its mount actions owns the combination
// directory (m<seq>).
func (e *Engine) staleRuntimeSession(
	name, runtimeDir string,
	health HealthFunc,
) (PathSession, error) {
	rec, err := e.Load(name)
	if errors.Is(err, ErrNotFound) {
		return PathSession{}, fmt.Errorf(
			"session: no session covers the runtime directory %s", runtimeDir)
	}
	if err != nil {
		return PathSession{}, err
	}
	if rec.State != StateActive {
		return PathSession{}, fmt.Errorf(
			"session: no session covers the runtime directory %s", runtimeDir)
	}
	want := filepath.Base(runtimeDir)
	for _, action := range rec.Actions {
		if action.Type != TypeMount || action.Status == StatusUnmounted {
			continue
		}
		id := action.RuntimeID
		if id == "" && action.Seq > 0 {
			id = runtimeIDOf(action.Seq)
		}
		if id == "" || id != want {
			continue
		}
		return PathSession{
			Record:     rec,
			Mountpoint: action.Mountpoint,
			Stale:      action.Mountpoint != "" && !health(action.Mountpoint),
		}, nil
	}
	return PathSession{}, fmt.Errorf(
		"session: no session covers the runtime directory %s", runtimeDir)
}

// viewRuntimeDir is the runtime directory a table entry's mount
// combination lives in: the parent of its upper for a writable
// view, or of its newest lower for a read-only chain, matching the
// layout the mount engine builds.
func viewRuntimeDir(mount mountinfo.Mount) string {
	switch {
	case mount.Upper != "" && filepath.Base(mount.Upper) == "upper":
		return filepath.Dir(mount.Upper)
	case mount.Upper == "" && len(mount.Lowers) > 0:
		return filepath.Dir(mount.Lowers[0])
	}
	return ""
}

// hasRuntimeSegment reports whether path falls under a
// <SessionsDir>/<name>/runtime/... combination.
func hasRuntimeSegment(sessionsRoot, path string) bool {
	_, _, ok := sessionRuntimeOf(sessionsRoot, path)
	return ok
}

// sessionRuntimeOf parses <SessionsDir>/<name>/runtime/<id>/... into
// the session name and the combination directory. Empty results mean
// the path is not a session-scoped runtime combination.
func sessionRuntimeOf(sessionsRoot, path string) (name, runtimeDir string, ok bool) {
	rel, err := filepath.Rel(sessionsRoot, path)
	if err != nil {
		return "", "", false
	}
	segments := strings.Split(rel, string(filepath.Separator))
	// <name>/runtime/<id> — at least three segments.
	if len(segments) < 3 || segments[0] == "." || segments[0] == ".." {
		return "", "", false
	}
	if segments[1] != "runtime" || segments[2] == "." || segments[2] == ".." {
		return "", "", false
	}
	name = segments[0]
	runtimeDir = filepath.Join(sessionsRoot, name, "runtime", segments[2])
	return name, runtimeDir, true
}

// recordForMountpoint scans the active records for one exact
// recorded mountpoint.
func (e *Engine) recordForMountpoint(point string) (Record, bool, error) {
	var (
		found Record
		hit   bool
	)
	err := e.eachRecord(func(rec Record) bool {
		if rec.State != StateActive {
			return false
		}
		for _, action := range rec.Actions {
			if action.Type == TypeMount && action.Status != StatusUnmounted &&
				action.Mountpoint == point {
				found, hit = rec, true
				return true
			}
		}
		return false
	})
	return found, hit, err
}

// eachRecord visits every record whose non-blocking scan lock is
// free, stopping when visit returns true. A record another process
// holds is skipped this round, and so is one removed after the
// listing; a corrupt record is reported, never silently dropped.
func (s Store) eachRecord(visit func(Record) bool) error {
	names, err := s.names()
	if err != nil {
		return err
	}
	for _, name := range names {
		release, ok := s.tryReadLock(name)
		if !ok {
			continue
		}
		rec, err := s.Load(name)
		release()
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if visit(rec) {
			return nil
		}
	}
	return nil
}

// tryReadLock takes one record's non-blocking scan lock. A busy
// record is skipped; a platform without advisory locking reads
// unlocked, which the atomic record write keeps safe.
func (s Store) tryReadLock(name string) (func(), bool) {
	release, err := s.tryLockSession(name)
	switch {
	case err == nil:
		return release, true
	case errors.Is(err, ErrLockHeld):
		return func() {}, false
	case errors.Is(err, ErrLockUnavailable):
		return func() {}, true
	default:
		return func() {}, false
	}
}

// normalizeForLookup resolves a path for the reverse lookup:
// readlink -f semantics first, falling back to the literal cleaned
// absolute path when normalization fails, which is how a deep path
// through a dead mount behaves. The lookup never gives up because
// normalization failed.
func normalizeForLookup(path string) (string, error) {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(resolved), nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("session: %w", err)
	}
	return filepath.Clean(abs), nil
}

// underPrefix reports whether path is prefix itself or lives below
// it, honoring the directory boundary.
func underPrefix(path, prefix string) bool {
	path, prefix = filepath.Clean(path), filepath.Clean(prefix)
	return path == prefix || strings.HasPrefix(path, prefix+string(filepath.Separator))
}

// names lists the recorded session names in the store directory,
// ordered by directory name. Each session owns one subdirectory; a
// missing sessions root lists nothing.
func (s Store) names() ([]string, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("session: %w", err)
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() || strings.EqualFold(entry.Name(), referenceDir) {
			continue
		}
		names = append(names, entry.Name())
	}
	return names, nil
}
