package image

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jiangxin/tiler/internal/filelock"
)

// DefaultLibraryLockTimeout bounds image-lock acquisition for version and
// reference changes. The constant name remains because every caller already
// uses it as the normal 30-second budget.
const DefaultLibraryLockTimeout = 30 * time.Second

const imageLockDir = ".locks"

// imageLockRetries bounds a list-then-lock retry when the repository set
// changes while the locks are being acquired.
const imageLockRetries = 8

// Hold is a set of per-image flocks, acquired in ascending name order.
// Release closes every descriptor. Discard unlinks one image's lock only
// after that repository directory is gone and the open inode still matches
// the directory entry.
type Hold struct {
	files []*os.File
	paths []string
	names []string
}

// Release unlocks and closes every file in the hold. It is safe on a zero Hold.
func (h Hold) Release() {
	for _, file := range h.files {
		if file == nil {
			continue
		}
		_ = filelock.Release(file)
		_ = file.Close()
	}
}

// Discard unlinks the lock file for name, and that image's journals beside
// it, while the caller still holds the flock. The unlink happens only when
// the path still names the inode this hold opened, so a replacement lock
// created for a new repository of the same name is left in place.
func (h Hold) Discard(name string) error {
	i := slices.Index(h.names, name)
	if i < 0 || h.files[i] == nil {
		return nil
	}
	held, err := h.files[i].Stat()
	if err != nil {
		return err
	}
	current, err := os.Stat(h.paths[i])
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !os.SameFile(held, current) {
		return nil
	}
	dir := filepath.Dir(h.paths[i])
	base := strings.TrimSuffix(filepath.Base(h.paths[i]), ".lock")
	for _, suffix := range []string{importTxnJournal, versionRemovalJournal} {
		path := filepath.Join(dir, base+suffix)
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := os.Remove(h.paths[i]); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncDir(dir, hooks{})
}

// DiscardIfRepositoryGone unlinks the image lock after the repository
// directory has been removed. A repository that still exists keeps its lock.
func (h Hold) DiscardIfRepositoryGone(imagesDir, name string) error {
	repo, err := OpenRepo(imagesDir, name)
	if err != nil {
		return err
	}
	_, err = os.Lstat(repo.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return h.Discard(name)
	}
	return err
}

// LockImage acquires the flock for one image name.
func LockImage(imagesDir, name string, timeout time.Duration) (Hold, error) {
	return LockImages(imagesDir, []string{name}, timeout)
}

// LockImages acquires flocks for names in ascending order. An empty name
// list holds nothing. A failure releases every flock already acquired.
func LockImages(imagesDir string, names []string, timeout time.Duration) (Hold, error) {
	if strings.TrimSpace(imagesDir) == "" {
		return Hold{}, fmt.Errorf("image lock: %w", ErrInvalidPath)
	}
	var hold Hold
	for _, name := range uniqueNames(names) {
		file, path, err := openImageLock(imagesDir, name, timeout)
		if err != nil {
			hold.Release()
			return Hold{}, err
		}
		hold.files = append(hold.files, file)
		hold.paths = append(hold.paths, path)
		hold.names = append(hold.names, name)
	}
	return hold, nil
}

// WithListedImageLocks lists repositories, locks those names, and retries
// when the set changes before the critical section starts. The hold is
// released when run returns.
func WithListedImageLocks(imagesDir string, timeout time.Duration, run func(Hold) error) error {
	if run == nil {
		return errors.New("image lock: missing callback")
	}
	if strings.TrimSpace(imagesDir) == "" {
		return run(Hold{})
	}
	var last error
	for range imageLockRetries {
		repos, err := ListRepos(imagesDir)
		if err != nil {
			return err
		}
		names := repoNames(repos)
		hold, err := LockImages(imagesDir, names, timeout)
		if err != nil {
			return err
		}
		again, err := ListRepos(imagesDir)
		if err != nil {
			hold.Release()
			return err
		}
		if !sameStrings(names, repoNames(again)) {
			hold.Release()
			last = errors.New("image lock: repository set changed")
			continue
		}
		err = run(hold)
		hold.Release()
		return err
	}
	if last == nil {
		last = errors.New("image lock: repository set changed")
	}
	return last
}

func openImageLock(imagesDir, name string, timeout time.Duration) (*os.File, string, error) {
	path, err := controlFile(imagesDir, name, ".lock")
	if err != nil {
		return nil, "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, "", err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, "", err
	}
	if err := filelock.Acquire(file, timeout); err != nil {
		_ = file.Close()
		return nil, "", fmt.Errorf("image lock: %w", err)
	}
	return file, path, nil
}

// controlFile is images/.locks/<segments>/<leaf><suffix>. A one-segment
// name stores the leaf directly under .locks.
func controlFile(imagesDir, name, suffix string) (string, error) {
	if strings.TrimSpace(imagesDir) == "" {
		return "", fmt.Errorf("image lock: %w", ErrInvalidPath)
	}
	segs, err := ParseName(name)
	if err != nil {
		return "", err
	}
	parts := append([]string{imagesDir, imageLockDir}, segs[:len(segs)-1]...)
	return filepath.Join(filepath.Join(parts...), segs[len(segs)-1]+suffix), nil
}

func uniqueNames(names []string) []string {
	seen := make(map[string]struct{}, len(names))
	out := make([]string, 0, len(names))
	for _, name := range names {
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

func repoNames(repos []Repo) []string {
	names := make([]string, len(repos))
	for i, repo := range repos {
		names[i] = repo.Name()
	}
	slices.Sort(names)
	return names
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// claimLegacyJournals moves a library-root journal into the image lock
// directory when its name field matches. A missing source, or a rename that
// loses the race, means another writer already claimed it.
func claimLegacyJournals(imagesDir, name string) error {
	for _, suffix := range []string{importTxnJournal, versionRemovalJournal} {
		if err := claimLegacyJournal(imagesDir, name, suffix); err != nil {
			return err
		}
	}
	return nil
}

func journalName(path string) string {
	body, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var probe struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(body, &probe) != nil {
		return ""
	}
	return probe.Name
}

func claimLegacyJournal(imagesDir, name, suffix string) error {
	legacy := filepath.Join(imagesDir, suffix)
	body, err := os.ReadFile(legacy)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var probe struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(body, &probe) != nil || probe.Name != name {
		return nil
	}
	dest, err := controlFile(imagesDir, name, suffix)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(dest); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if err := os.Rename(legacy, dest); err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrExist) {
			return nil
		}
		return err
	}
	return nil
}
