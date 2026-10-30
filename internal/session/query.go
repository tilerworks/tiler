package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Query selects sessions by their recorded mount directories.
type Query struct {
	Directory string
}

// Query returns complete matching session views from one host snapshot.
// Unlike reverse lookup, either directory may contain the other.
func (e *Engine) Query(current string, query Query) (Overview, error) {
	directory := ""
	if query.Directory != "" {
		info, err := os.Stat(query.Directory)
		if err == nil && !info.IsDir() {
			return Overview{}, fmt.Errorf("session: directory required: %s", query.Directory)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return Overview{}, err
		}
		directory, err = canonicalQueryPath(query.Directory)
		if err != nil {
			return Overview{}, err
		}
	}
	overview, err := e.Overview(current)
	if err != nil {
		return Overview{}, err
	}
	selected := overview.Sessions[:0]
	for _, view := range overview.Sessions {
		if view.State == StateClosed && !view.Locked {
			// Read again under the scan lock; closed records have no
			// expanded actions in the existing Overview contract.
			release, ok := e.tryReadLock(view.Name)
			if ok {
				rec, readErr := e.Load(view.Name)
				release()
				if errors.Is(readErr, ErrNotFound) {
					continue
				}
				if readErr != nil {
					return Overview{}, readErr
				}
				for _, action := range rec.Actions {
					view.Actions = append(view.Actions, ActionView{Action: action})
				}
			} else {
				view = View{Summary: Summary{Name: view.Name}, Locked: true}
			}
		}
		if directory == "" || matchesDirectory(view, directory) {
			selected = append(selected, view)
		}
	}
	overview.Sessions = selected
	orphans := overview.Orphans[:0]
	for _, orphan := range overview.Orphans {
		if directory == "" || relatedQueryPath(orphan.Point, directory) {
			orphans = append(orphans, orphan)
		}
	}
	overview.Orphans = orphans
	return overview, nil
}

func matchesDirectory(view View, directory string) bool {
	for _, action := range view.Actions {
		if action.Type == TypeMount && action.Mountpoint != "" && relatedQueryPath(action.Mountpoint, directory) {
			return true
		}
	}
	return false
}

func relatedQueryPath(path, directory string) bool {
	normalized, err := canonicalQueryPath(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(directory, normalized)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return true
	}
	rel, err = filepath.Rel(normalized, directory)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// canonicalQueryPath also resolves existing ancestors of vanished paths.
func canonicalQueryPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	for ancestor := abs; ; ancestor = filepath.Dir(ancestor) {
		resolved, err := filepath.EvalSymlinks(ancestor)
		if err == nil {
			tail, err := filepath.Rel(ancestor, abs)
			return filepath.Join(resolved, tail), err
		}
		if !errors.Is(err, os.ErrNotExist) || filepath.Dir(ancestor) == ancestor {
			return "", err
		}
	}
}
