package image

import (
	"errors"
	"os"
	"path/filepath"
)

// State is the listing of one repository: its active image plus the
// chains parked beside it.
type State struct {
	// Active is the active image metadata. HasActive reports whether
	// an active image loaded; a repository mid-publish or fully
	// archived has none.
	Active    Meta
	HasActive bool
	// Archives and Holds list the chain ids under _archives/ and
	// _hold/, sorted by name. Transient parked entries never list.
	Archives []string
	Holds    []string
}

// ListRepos enumerates every valid owner/repo directory under the
// image library. Entries that do not parse as names are skipped.
func ListRepos(imagesDir string) ([]Repo, error) {
	owners, err := os.ReadDir(imagesDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var repos []Repo
	for _, owner := range owners {
		if !owner.IsDir() {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(imagesDir, owner.Name()))
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			repo, err := OpenRepo(imagesDir, owner.Name()+"/"+entry.Name())
			if err != nil {
				continue
			}
			repos = append(repos, repo)
		}
	}
	return repos, nil
}

// Inspect collects the listing state of one repository. An unreadable
// archive still lists by id; a corrupt active meta fails the call.
func Inspect(repo Repo) (State, error) {
	var state State
	meta, exists, err := loadActive(repo)
	if err != nil {
		return State{}, err
	}
	state.Active, state.HasActive = meta, exists
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
// lexical order. Foreign and transient entries are skipped.
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
		if entry.IsDir() && checkID(entry.Name()) == nil {
			ids = append(ids, entry.Name())
		}
	}
	return ids, nil
}
