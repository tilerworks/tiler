package image

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// State is the listing of one repository: its active manifest plus
// the chains parked beside it.
type State struct {
	// Active is the active image manifest. HasActive reports whether
	// an active manifest loaded; a repository mid-publish or fully
	// archived has none.
	Active    Manifest
	HasActive bool
	// Archives and Holds list the chain ids under _archives/ and
	// _hold/, sorted by name. Transient parked entries never list.
	Archives []string
	Holds    []string
}

// ListRepos enumerates every leaf repository under the image
// library, walking at most maxNameSegments deep. A directory that
// carries a leaf marker is recorded and not descended into.
// Namespace directories are not listed. Entries whose names are not
// valid segments are skipped.
func ListRepos(imagesDir string) ([]Repo, error) {
	if _, err := os.Stat(imagesDir); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var repos []Repo
	if err := walkRepos(imagesDir, nil, &repos); err != nil {
		return nil, err
	}
	return repos, nil
}

func walkRepos(imagesDir string, segs []string, out *[]Repo) error {
	dir := imagesDir
	if len(segs) > 0 {
		dir = filepath.Join(append([]string{imagesDir}, segs...)...)
	}
	if len(segs) > 0 && isLeafDir(dir) {
		repo, err := OpenRepo(imagesDir, strings.Join(segs, "/"))
		if err != nil {
			return nil
		}
		*out = append(*out, repo)
		return nil
	}
	if len(segs) >= maxNameSegments {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if checkSegment(entry.Name()) != nil {
			continue
		}
		next := append(append([]string{}, segs...), entry.Name())
		if err := walkRepos(imagesDir, next, out); err != nil {
			return err
		}
	}
	return nil
}

// isLeafDir reports whether dir holds a leaf layout marker: a meta
// or image file, a layout subdirectory, or any name starting with
// ".". A missing or unreadable directory is not a leaf.
func isLeafDir(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		name := entry.Name()
		switch name {
		case fileMeta, ImageFile, ImageDMG, dirIncr, dirArchives, dirHold, dirBlocks:
			return true
		}
		if strings.HasPrefix(name, ".") {
			return true
		}
	}
	return false
}

// checkNamePlacement refuses a publish name whose ancestor is already
// a leaf, or whose directory already contains a deeper leaf.
func checkNamePlacement(imagesDir string, segments []string) error {
	name := strings.Join(segments, "/")
	for i := 1; i < len(segments); i++ {
		prefix := segments[:i]
		dir := filepath.Join(append([]string{imagesDir}, prefix...)...)
		if isLeafDir(dir) {
			return fmt.Errorf("%w: %q conflicts with existing leaf %q",
				ErrNameConflict, name, strings.Join(prefix, "/"))
		}
	}
	target := filepath.Join(append([]string{imagesDir}, segments...)...)
	if deeper, ok := findDeeperLeaf(target, segments); ok {
		return fmt.Errorf("%w: %q conflicts with existing leaf %q",
			ErrNameConflict, name, deeper)
	}
	return nil
}

// findDeeperLeaf walks valid name-segment children under dir looking
// for a leaf that is a proper descendant of segments.
func findDeeperLeaf(dir string, segments []string) (string, bool) {
	if len(segments) >= maxNameSegments {
		return "", false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	for _, entry := range entries {
		if !entry.IsDir() || checkSegment(entry.Name()) != nil {
			continue
		}
		childSegs := append(append([]string{}, segments...), entry.Name())
		child := filepath.Join(dir, entry.Name())
		if isLeafDir(child) {
			return strings.Join(childSegs, "/"), true
		}
		if deeper, ok := findDeeperLeaf(child, childSegs); ok {
			return deeper, true
		}
	}
	return "", false
}

// Inspect collects the listing state of one repository. An unreadable
// archive still lists by id; a corrupt active meta fails the call.
func Inspect(repo Repo) (State, error) {
	var state State
	manifest, exists, err := loadActiveManifest(repo)
	if err != nil {
		return State{}, err
	}
	state.Active, state.HasActive = manifest, exists
	state.Archives, err = chainDirIDs(filepath.Join(repo.Dir, dirArchives))
	if err != nil {
		return State{}, err
	}
	state.Holds, err = listHoldIDs(repo)
	if err != nil {
		return State{}, err
	}
	return state, nil
}

// chainDirIDs lists the valid chain directory names under dir in
// lexical order. Full sha256 names and legacy 16-hex names count;
// foreign and transient entries are skipped.
func chainDirIDs(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, entry := range entries {
		if entry.IsDir() && onDiskID(entry.Name()) {
			ids = append(ids, entry.Name())
		}
	}
	return ids, nil
}
