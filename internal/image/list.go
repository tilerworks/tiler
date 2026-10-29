package image

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

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
		case fileMeta, ImageFile, ImageDMG, dirIncr, dirBlocks, dirObjects:
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

func loadActiveManifest(repo Repo) (Manifest, bool, error) {
	doc, err := LoadVersionManifest(repo)
	if errors.Is(err, os.ErrNotExist) {
		return Manifest{}, false, nil
	}
	if err != nil {
		return Manifest{}, false, err
	}
	return manifestFromVersion(doc), true, nil
}
