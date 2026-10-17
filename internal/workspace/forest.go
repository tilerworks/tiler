package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ForestMember is one partition of a members-form split payload.
type ForestMember struct {
	// Name is the partition name; the member mounts at
	// <ParentDir>/<Name>.
	Name string
	// Path is the member squashfs file to mount.
	Path string
	// FSName overrides the identity published in the mount table.
	// Empty keeps Path, which is the members-form default: every
	// member file is a real library file.
	FSName string
}

// ForestRequest describes one members-form forest mount: every
// partition mounts read-only at its own subpath under one shared
// parent directory, so the view is the side-by-side union of the
// partitions. The forest is one mount combination: the parent
// directory is the combination's point, the member submounts join
// and leave together, and no runtime state is derived.
type ForestRequest struct {
	// Partitions lists the members in manifest order, which is the
	// mount order and the reverse teardown order.
	Partitions []ForestMember
	// ParentDir is the directory the member subpaths hang under.
	// It may already hold content: the forest never hides the
	// parent itself.
	ParentDir string
	// SquashfsOptions is appended to the squashfuse_ll options of
	// every member after the built-in ones.
	SquashfsOptions string
	// Table is an injected mount table snapshot. Nil skips the
	// table-driven reuse and conflict checks.
	Table []Entry
	// Owned reports whether a mount source resolves inside the
	// image library. When set, a listed-but-dead entry at a member
	// point that is neither this member's identity nor a library
	// source is refused instead of torn down, so a foreign dead
	// mount is never silently removed. Nil cleans any dead entry,
	// matching the single-point engine.
	Owned func(source string) bool
	// Run executes external tools. Nil runs them through exec.
	Run func(name string, args ...string) error
}

// MountForest mounts a members-form partition forest.
//
// Each member mounts read-only with squashfuse_ll at <ParentDir>/<Name>,
// publishing the member file path as the mount table identity so the
// combination stays recognizable across hosts and replays. A member
// point whose live entry matches that identity is reused untouched,
// which makes the whole call idempotent: a replay of a healthy
// forest creates nothing, a partially dead forest cleans and remounts
// only the dead members, and missing members join the surviving ones.
// A conflicting identity, a non-empty plain point, or a foreign
// mount at a member point is refused without hiding anything.
//
// Any failure tears down every mount and removes every directory
// this call created. Non-Linux hosts, a missing /dev/fuse, and
// missing tools return ErrUnavailable.
func MountForest(req ForestRequest) (*Workspace, error) {
	run := req.Run
	if run == nil {
		run = runTool
	}
	return mountForest(req, run, hooks{})
}

func mountForest(req ForestRequest, run runFunc, seam hooks) (*Workspace, error) {
	if err := preflightForest(); err != nil {
		return nil, err
	}
	if req.ParentDir == "" || len(req.Partitions) == 0 {
		return nil, errors.New(
			"workspace: a forest needs a parent dir and at least one partition")
	}
	for _, m := range req.Partitions {
		if err := checkForestMember(m); err != nil {
			return nil, err
		}
	}
	w := &Workspace{MergedDir: req.ParentDir, run: run}
	if err := w.prepareParent(req); err != nil {
		return nil, err
	}
	reusedAll := true
	for _, m := range req.Partitions {
		reused, err := w.mountMember(req, m, seam)
		if err != nil {
			return nil, err
		}
		if !reused {
			reusedAll = false
		}
	}
	w.Reused = reusedAll
	return w, nil
}

// preflightForest checks the FUSE device and the two tools every
// member mount needs. The forest merges nothing and probes no
// subdirs, so no overlay and no unsquashfs are required.
func preflightForest() error {
	if err := available(); err != nil {
		return err
	}
	return lookTools("squashfuse_ll", "fusermount3")
}

// checkForestMember guards the member shape before any tool runs:
// the name becomes a subpath of the parent, so it must be one safe
// segment, and there must be bytes to mount.
func checkForestMember(m ForestMember) error {
	if m.Name == "" || m.Name == "." || m.Name == ".." ||
		strings.ContainsAny(m.Name, `/\`) {
		return fmt.Errorf(
			"workspace: member name %q must be a single path segment", m.Name)
	}
	if m.Path == "" {
		return fmt.Errorf("workspace: member %s has no image path", m.Name)
	}
	return nil
}

// prepareParent creates a missing parent directory under the
// rollback accounting and guards an existing one: it must be a
// directory owned by the current user. Its content is left alone —
// the forest mounts into the parent, never over it.
func (w *Workspace) prepareParent(req ForestRequest) error {
	info, err := os.Stat(req.ParentDir)
	if errors.Is(err, os.ErrNotExist) {
		if err := w.mkdirNew(req.ParentDir); err != nil {
			return w.fail(err)
		}
		return nil
	}
	if err != nil {
		return w.fail(fmt.Errorf("workspace: %w", err))
	}
	if !info.IsDir() {
		return w.fail(fmt.Errorf("workspace: %s is not a directory", req.ParentDir))
	}
	if err := checkOwner(fileUID(info), uint32(os.Geteuid())); err != nil {
		return w.fail(err)
	}
	return nil
}

// mountMember brings one member point into the combination and
// reports whether the existing mount was reused. Reuse needs the
// table: a live entry with the member identity is adopted as it is;
// every other path ends in a fresh squashfuse_ll mount that joins
// the teardown list.
func (w *Workspace) mountMember(req ForestRequest, m ForestMember, seam hooks) (bool, error) {
	point := filepath.Join(req.ParentDir, m.Name)
	fsname := m.FSName
	if fsname == "" {
		fsname = m.Path
	}
	entry, listed := findEntry(req.Table, point)
	switch {
	case listed && servingOf(seam, point):
		if entry.Source != fsname {
			return false, w.fail(conflictErr(point, entry))
		}
		return true, nil
	case listed:
		if entry.Source != fsname && req.Owned != nil && !req.Owned(entry.Source) {
			return false, w.fail(conflictErr(point, entry))
		}
		if err := cleanPoint(point, w.run, seam); err != nil {
			return false, w.fail(err)
		}
	default:
		if liveOf(seam, point) {
			return false, w.fail(fmt.Errorf(
				"workspace: %s is already a mount the table does not list; "+
					"refusing to hide it under member %s", point, m.Name))
		}
		if err := w.preparePoint(point); err != nil {
			return false, w.fail(err)
		}
	}
	if err := w.mountPoint(forestMember(m, fsname), memberRequest(req), point, seam); err != nil {
		return false, err
	}
	return false, nil
}

// preparePoint creates a missing member point under the rollback
// accounting and guards an existing one with the target discipline:
// an empty directory is accepted, anything with content is refused
// so a member mount never hides user data.
func (w *Workspace) preparePoint(point string) error {
	if _, err := os.Stat(point); errors.Is(err, os.ErrNotExist) {
		if err := w.mkdirNew(point); err != nil {
			return err
		}
	}
	return guardTarget(point)
}

// forestMember adapts one partition to the member shape the shared
// squashfuse_ll mount path consumes.
func forestMember(m ForestMember, fsname string) member {
	return member{path: m.Path, fsname: fsname}
}

// memberRequest adapts the forest request to the fields the shared
// mount path reads: the option string only, since a forest member
// is always a whole-image read-only mount.
func memberRequest(req ForestRequest) Request {
	return Request{SquashfsOptions: req.SquashfsOptions}
}
