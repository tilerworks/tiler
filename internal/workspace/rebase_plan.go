package workspace

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"

	"github.com/jiangxin/tiler/internal/dirroot"
	"github.com/jiangxin/tiler/internal/durable"
)

const rebaseJournal = "rebase.json"

// RebaseIdentity binds recovery to one session incarnation and stack transition.
type RebaseIdentity struct {
	Instance    string `json:"instance"`
	Image       string `json:"image"`
	Runtime     string `json:"runtime"`
	ServedFull  string `json:"served_full"`
	ServedTip   string `json:"served_tip"`
	DesiredFull string `json:"desired_full"`
	DesiredTip  string `json:"desired_tip"`
}

// UpperRebase supplies the still-served stack for preserving post-build deletions.
// Members have the same root-to-tip order as Request.Members.
type UpperRebase struct {
	Identity RebaseIdentity
	Members  []MountMember
}

type rebasePlan struct {
	Version  int            `json:"version"`
	Identity RebaseIdentity `json:"identity"`
	Deleted  []string       `json:"deleted"`
	Applied  bool           `json:"applied"`
	Ready    bool           `json:"ready"`
}

// planRebaseDeletions compares visible trees, never absence from the upper alone.
// Walking both directory trees with Lstat avoids following user symlinks.
func planRebaseDeletions(current, desired string) ([]string, error) {
	c, err := dirroot.Open(current)
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Close() }()
	d, err := dirroot.Open(desired)
	if err != nil {
		return nil, err
	}
	defer func() { _ = d.Close() }()
	var deleted []string
	var visit func(string) error
	visit = func(dir string) error {
		entries, err := fs.ReadDir(d.FS(), dir)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			rel := path.Join(dir, entry.Name())
			have, statErr := c.Lstat(rel)
			if errors.Is(statErr, os.ErrNotExist) {
				deleted = append(deleted, rel)
				continue // A removed directory hides its whole subtree.
			}
			if statErr != nil {
				return statErr
			}
			if entry.IsDir() && have.IsDir() {
				if err := visit(rel); err != nil {
					return err
				}
			}
		}
		return nil
	}
	err = visit(".")
	return deleted, err
}

func validateRebasePlan(plan rebasePlan) error {
	if plan.Version != 1 || (plan.Ready && !plan.Applied) ||
		plan.Identity.Instance == "" || plan.Identity.Image == "" || plan.Identity.Runtime == "" ||
		plan.Identity.ServedFull == "" || plan.Identity.ServedTip == "" ||
		plan.Identity.DesiredFull == "" || plan.Identity.DesiredTip == "" {
		return errors.New("invalid upper rebase journal identity or phase")
	}
	seen := make(map[string]bool, len(plan.Deleted))
	for _, rel := range plan.Deleted {
		if !fs.ValidPath(rel) || rel == "." || seen[rel] {
			return fmt.Errorf("invalid upper rebase deletion %q", rel)
		}
		seen[rel] = true
	}
	for rel := range seen {
		for parent := path.Dir(rel); parent != "."; parent = path.Dir(parent) {
			if seen[parent] {
				return fmt.Errorf("overlapping upper rebase deletion %q", rel)
			}
		}
	}
	return nil
}

func loadRebasePlan(runtime string) (*rebasePlan, error) {
	root, err := dirroot.Open(runtime)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	info, err := root.Lstat(rebaseJournal)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("upper rebase journal is not a regular file")
	}
	file, err := root.Open(rebaseJournal)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	dec := json.NewDecoder(file)
	dec.DisallowUnknownFields()
	var plan rebasePlan
	if err := dec.Decode(&plan); err != nil {
		return nil, err
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, errors.New("upper rebase journal must contain one document")
	}
	return &plan, validateRebasePlan(plan)
}

func saveRebasePlan(runtime string, plan rebasePlan) error {
	if err := validateRebasePlan(plan); err != nil {
		return err
	}
	body, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	root, err := dirroot.Open(runtime)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	token, err := rebaseTempToken()
	if err != nil {
		return err
	}
	tmp := ".rebase-" + token
	file, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = root.Remove(tmp) }()
	_, writeErr := file.Write(append(body, '\n'))
	if writeErr == nil {
		writeErr = durable.Sync(file)
	}
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return err
	}
	if err := root.Rename(tmp, rebaseJournal); err != nil {
		return err
	}
	return syncRebaseDirectory(root)
}

func syncRebaseDirectory(root *dirroot.Root) error {
	file, err := root.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(durable.Sync(file), file.Close())
}

// RebaseReady identifies a mount completed before its session writeback failed.
func RebaseReady(runtime string, identity RebaseIdentity) (bool, error) {
	plan, err := loadRebasePlan(runtime)
	if err != nil || plan == nil {
		return false, err
	}
	if plan.Identity != identity {
		return false, errors.New("upper rebase journal belongs to another transition")
	}
	return plan.Ready, nil
}

// CompleteRebase removes recovery state only after the desired pin is durable.
// Callers hold the session lock and pass the newly saved, served-free identity.
func CompleteRebase(runtime string, identity RebaseIdentity) error {
	plan, err := loadRebasePlan(runtime)
	if err != nil || plan == nil {
		return err
	}
	identity.ServedFull, identity.ServedTip = plan.Identity.ServedFull, plan.Identity.ServedTip
	if plan.Identity != identity || !plan.Ready {
		return errors.New("upper rebase journal does not match the recorded mount")
	}
	root, err := dirroot.Open(runtime)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if err := root.Remove(rebaseJournal); err != nil {
		return err
	}
	return syncRebaseDirectory(root)
}

func pendingRebasePlan(runtime string, identity RebaseIdentity) (*rebasePlan, error) {
	plan, err := loadRebasePlan(runtime)
	if err != nil {
		return nil, err
	}
	if plan != nil && plan.Identity != identity {
		return nil, errors.New("upper rebase journal belongs to another transition")
	}
	return plan, nil
}

// Keep plans deterministic for recovery diagnostics and tests.
func newRebasePlan(identity RebaseIdentity, deleted []string) rebasePlan {
	copied := append([]string(nil), deleted...)
	slices.Sort(copied)
	return rebasePlan{Version: 1, Identity: identity, Deleted: copied}
}

func rebaseTempToken() (string, error) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
	var src [26]byte
	if _, err := rand.Read(src[:]); err != nil {
		return "", err
	}
	for i, b := range src {
		src[i] = alphabet[b%32]
	}
	return string(src[:]), nil
}
