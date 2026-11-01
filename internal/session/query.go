package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jiangxin/tiler/internal/mountinfo"
)

// Query selects sessions by directory, name, image stack, and activity.
type Query struct {
	Directory string
	Session   string
	Image     string
	// Activity accepts active, inactive, or empty for every session.
	Activity ActivityClass
}

// Query returns complete matching session views from one host snapshot.
// Unlike reverse lookup, either directory may contain the other.
func (e *Engine) Query(current string, query Query) (Overview, error) {
	if query.Activity != "" && query.Activity != ActivityActive && query.Activity != ActivityInactive {
		return Overview{}, fmt.Errorf("session: invalid activity filter: %q", query.Activity)
	}
	if query.Session != "" {
		if err := ValidateName(query.Session); err != nil {
			return Overview{}, err
		}
	}
	filter, err := e.queryImage(query.Image)
	if err != nil {
		return Overview{}, err
	}
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
	var table []mountinfo.Mount
	var diskImages []DiskImage
	tableAvailable := true
	scan := *e
	if e.useDiskImages() {
		diskImages, err = e.diskImages()
		if err != nil {
			return Overview{}, err
		}
		scan.LoadDiskImages = func() ([]DiskImage, error) { return diskImages, nil }
	} else {
		table, err = e.loadTable()
		if errors.Is(err, mountinfo.ErrUnavailable) {
			table, err, tableAvailable = nil, nil, false
		}
		if err != nil {
			return Overview{}, err
		}
		scan.LoadTable = func() ([]mountinfo.Mount, error) { return table, nil }
	}
	overview, err := scan.Overview(current)
	if err != nil {
		return Overview{}, err
	}
	selected := overview.Sessions[:0]
	found := query.Session == ""
	for _, view := range overview.Sessions {
		if view.Name == query.Session {
			found = true
		}
	}
	if !found {
		return Overview{}, fmt.Errorf("session: %s: %w", query.Session, ErrNotFound)
	}
	for _, view := range overview.Sessions {
		if query.Session != "" && query.Session != view.Name {
			continue
		}
		if view.State == StateClosed && !view.Locked {
			// Read again under the scan lock; closed records have no
			// expanded actions in the existing Overview contract.
			release, ok := e.tryReadLock(view.Name)
			if ok {
				rec, readErr := e.Show(view.Name)
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
		if matchesQuery(view, directory, filter, e.ImagesDir, &overview.Diagnostics) {
			view.Mounts = e.mountDetails(view, table)
			if e.useDiskImages() {
				e.annotateDiskDetails(view.Name, view.Mounts, diskImages)
				view.ActivityClass, view.ActivityReasons = classifyActivity(view.State, view.Locked, e.diskActivity(view, diskImages))
			} else {
				evidence := e.tableActivity(view, table)
				evidence.Available = tableAvailable
				view.ActivityClass, view.ActivityReasons = classifyActivity(view.State, view.Locked, evidence)
			}
			if query.Activity == "" || view.ActivityClass == ActivityUnknown || view.ActivityClass == query.Activity {
				selected = append(selected, view)
			}
		}
	}
	overview.Sessions = selected
	orphans := overview.Orphans[:0]
	for _, orphan := range overview.Orphans {
		if query.Session != "" || query.Image != "" || query.Activity == ActivityInactive {
			continue
		}
		if directory == "" || relatedQueryPath(orphan.Point, directory) {
			orphans = append(orphans, orphan)
		}
	}
	overview.Orphans = orphans
	return overview, nil
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
