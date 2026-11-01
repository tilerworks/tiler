// Package workspace mounts read-only container images and their
// incremental chains as read-only or writable merged views. It
// serves both the incremental build quality gate and the formal
// mount flow: no cobra command is registered here, and no session
// record is written.
package workspace

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jiangxin/tiler/internal/container"
)

// ErrUnavailable means mounting cannot work here: the host is not
// Linux or macOS, or Linux is missing /dev/fuse or a FUSE tool.
// Callers treat it as a skip. A macOS disk image does not use it
// when hdiutil is selected.
var ErrUnavailable = errors.New("workspace: mounting requires linux and a usable /dev/fuse")

// maxStackDepth is one full image plus at most seven increments.
const maxStackDepth = 8

const (
	// liveInterval is the pause between mount liveness polls.
	liveInterval = 100 * time.Millisecond
	// livePolls bounds how long one mount may take to come up:
	// one hundred polls at the interval make a ten second bound.
	livePolls = 100
)

// runFunc executes one external tool by name and arguments.
type runFunc func(name string, args ...string) error

// Entry is one mount table row reduced to what the engine needs. The
// command layer converts rows from the mount table parser and injects
// them; the engine never imports that package, so the image package's
// internal tests cannot form an import cycle.
type Entry struct {
	// Point is the mount point path.
	Point string
	// FSType is the filesystem type recorded in the mount table.
	FSType string
	// Source is the mounted source or fsname identity.
	Source string
	// Upper is the overlay upperdir option, empty without one.
	Upper string
	// Lowers lists the overlay lowerdir options, newest first.
	Lowers []string
}

// Request describes one mount. The zero values of the optional
// fields keep the writable merged view the incremental build and its
// quality gate rely on.
type Request struct {
	// Members overrides the legacy Full/Layers fields with a root-to-tip
	// stack. Concat cannot be combined with explicit members.
	Members []MountMember
	// Full is the full image file mounted as the base layer.
	Full string
	// Layers lists incremental image files in chain order, oldest
	// first. Mounting reverses them so the newest layer wins.
	Layers []string
	// FullFSName overrides the identity published in the mount
	// table when the mounted bytes are a cache copy. Empty keeps
	// Full as the identity.
	FullFSName string
	// LayerFSNames overrides layer identities, indexed like Layers.
	// An empty entry keeps that layer's path as the identity.
	LayerFSNames []string
	// Format is the legacy container type of every stack layer, for example
	// "squashfs" or "erofs". Library mounts copy it from metadata
	// after the file magic agrees. File-path mounts take it from
	// detection. Empty is refused.
	Format string
	// ReadOnly mounts without an upper: a single member goes
	// straight onto MergedDir, and a chain merges lowerdir-only.
	ReadOnly bool
	// PruneUpper asks a read-write mount to prune a reused upper
	// against the lower stack before merging: when the upper
	// directory already exists with content, a temporary read-only
	// merge of the lowers comes up at a runtime transit point, the
	// upper entries that merge already serves identically are
	// removed, and the temporary point is torn down again. Every
	// prune failure only warns on stderr and never blocks the
	// mount. Read-only requests ignore it.
	PruneUpper bool
	// Rebase preserves deletions before switching from a served stack. Unlike
	// redundant-file pruning, this must succeed even when the upper is empty.
	Rebase *UpperRebase
	// Progress receives the upper-prune lines. Nil writes nothing,
	// so callers that do not care stay quiet. The text is
	// "pruning upper ...", then "pruning upper ... N%" while the
	// integer percent climbs through 1..99, then "pruning upper ...
	// done" after the window succeeds. A character-device file is
	// rewritten in place; every other writer gets one line per
	// update. A failed window keeps the stderr warning and omits
	// done.
	Progress io.Writer
	// Subdir is an image-relative prefix exposed as the mount
	// root. Empty mounts whole images.
	Subdir string
	// Table is an injected mount table snapshot. Nil skips the
	// table-driven checks.
	Table []Entry
	// SquashfsOptions is the format-specific option string passed to
	// MountArgv. Squashfs appends it after the built-in options.
	// Erofs ignores it in this version.
	SquashfsOptions string
	// FuseOptions is appended to the fuse-overlayfs options after
	// the built-in ones.
	FuseOptions string
	// RuntimeDir is the session-scoped combination directory for
	// this mount (sessions/<name>/runtime/m<seq>). Read-only transit
	// points and the upper and work directories live directly under
	// it. The caller assigns the path; the engine does not derive it
	// from the mountpoint's device and inode.
	RuntimeDir string
	// MergedDir is where the merged view is mounted.
	MergedDir string
	// Concat selects the optional zero-reassembly route for a
	// byte-block payload: concatfs mounts the block directory at a
	// runtime transit point, and the base layer mounts the virtual
	// concatenated file instead of a materialized copy. Nil keeps
	// the plain single-file behavior.
	Concat *ConcatSource
	// Run executes external tools. Nil runs them through exec.
	Run func(name string, args ...string) error
	// Hdiutil runs hdiutil for a dmg mount and returns stdout. Nil
	// selects the platform exec: hdiutil on PATH on darwin, and
	// ErrUnavailable on every other host. FUSE mounts ignore it.
	Hdiutil func(argv []string, consErr error) ([]byte, error)
}

// MountMember carries one layer's payload path, optional published identity,
// and container type. Split payloads can provide resolved Blocks instead.
type MountMember struct {
	Path   string
	FSName string
	Format string
	// Blocks lists resolved payload files for a concatfs view, in byte order.
	// Empty mounts Path directly. Path remains the canonical descriptor name.
	Blocks []string
}

// ConcatSource describes the concat view of a byte-block payload
// directory.
type ConcatSource struct {
	// BlocksDir holds the byte blocks and the concat description
	// file.
	BlocksDir string
	// ViewName is the description file name that concatfs exposes
	// as the virtual concatenated image file.
	ViewName string
}

// Workspace is a mounted merged view and its runtime state.
type Workspace struct {
	// MergedDir is the view mount point.
	MergedDir string
	// UpperDir holds the changes an incremental build packs. It is
	// empty for a read-only mount.
	UpperDir string
	// Reused reports that this call found an existing mount with
	// the same identity and created nothing new.
	Reused bool
	// Prune reports the upper prune this mount ran before merging.
	// It is nil when no prune was attempted: a read-only or reused
	// mount, PruneUpper unset, a fresh or empty upper, or a prune
	// window whose temporary merge never came up.
	Prune *PruneReport
	// PruneFailed reports that the prune window was entered for a
	// non-empty upper and did not succeed. The view still mounts.
	// The existing stderr warning is the failure signal, and a nil
	// Progress writer stays silent. An empty upper, a request that
	// did not ask to prune, and a window that finished leave it false.
	PruneFailed bool

	runtimeDir string
	mounts     []string
	created    []string
	run        runFunc
	// dmg selects hdiutil detach instead of fusermount3.
	dmg bool
	// hdiutil runs the disk-image tool for a dmg view.
	hdiutil hdiutilRun
}

// hooks are test seams. The zero value polls the real mount state on
// the production clock.
type hooks struct {
	// live reports whether a point currently behaves as a mount.
	live func(point string) bool
	// serving reports whether a point behaves as a mount whose FUSE
	// connection still answers. Nil falls back to live, then to the
	// real probes.
	serving func(point string) bool
	// sleep pauses between liveness polls.
	sleep func(d time.Duration)
	// prune compares one upper directory with a read-only lower
	// view and removes the redundant entries. Nil runs the real
	// PruneUpper.
	prune func(upperDir, lowerView string) (PruneReport, error)
}

// member is one layer of the stack: the bytes to mount, the identity
// published in the mount table, and the container type.
type member struct {
	path   string
	fsname string
	format string
	blocks []string
}

// Mount mounts the requested view.
//
// Each layer is mounted read-only through its container's MountArgv,
// newest increment first and the full image last, passing the
// original image path as the FUSE source name so the mount table can
// identify the image even when the mounted bytes are a cache copy.
// A read-write view merges the stack with fuse-overlayfs over an
// upper and a work directory under the caller-supplied combination
// directory (sessions/<name>/runtime/m<seq>/); a read-only chain
// merges lowerdir-only, and a read-only single image mounts straight
// onto the target. Split members also need runtime concat mounts. When
// Subdir is set, every layer is checked with HasSubdir and layers missing
// the subdirectory are dropped before mounting their containers.
//
// Members with Blocks mount concatfs views at runtime transit points
// before mounting any container, so rollback detaches their dependents
// first. The legacy Concat field provides a single base-layer view.
//
// With PruneUpper set on a read-write request whose upper directory
// already exists with content, the mounted lowers are merged
// read-only at a temporary transit point first, the upper entries
// that merge serves identically are pruned, and the temporary point
// is torn down before the merged view is built — the only window
// where the upper belongs to no live view. Prune problems only warn
// on stderr; they never block the mount.
//
// Both FUSE tools daemonize, so every exec is followed by a poll
// that confirms the point is a live mount. A failing or vanished
// mount tears down every point and directory this call created and
// reports the primary error. A dmg request attaches with hdiutil
// and does not build this FUSE stack. Hosts other than Linux and
// macOS, a missing /dev/fuse, and missing FUSE tools return
// ErrUnavailable.
func Mount(req Request) (*Workspace, error) {
	run := req.Run
	if run == nil {
		run = runTool
	}
	return mount(req, run, hooks{})
}

func mount(req Request, run runFunc, seam hooks) (*Workspace, error) {
	req = memberRequest(req)
	if req.Format == container.DMG().Type() {
		return mountDMG(req, seam)
	}
	if err := preflight(req); err != nil {
		return nil, err
	}
	if req.Full == "" || req.RuntimeDir == "" || req.MergedDir == "" {
		return nil, errors.New("workspace: full image, runtime dir, and merged dir are required")
	}
	if req.Concat != nil && (req.Concat.BlocksDir == "" || req.Concat.ViewName == "") {
		return nil, errors.New("workspace: concat source needs a blocks dir and a view name")
	}
	if err := CheckSubdir(req.Subdir); err != nil {
		return nil, err
	}
	members, err := stack(req)
	if err != nil {
		return nil, err
	}
	if underPrefix(req.MergedDir, req.RuntimeDir) {
		return nil, fmt.Errorf("workspace: %s is inside the runtime tree %s",
			req.MergedDir, req.RuntimeDir)
	}
	// The reverse nesting would place upper/work/lower under the
	// merged view and create an overlay self-reference.
	if underPrefix(req.RuntimeDir, req.MergedDir) {
		return nil, fmt.Errorf("workspace: runtime tree %s is inside the mount target %s",
			req.RuntimeDir, req.MergedDir)
	}
	if !hasBlockMembers(members) {
		members, err = pruneBySubdir(members, req, run)
		if err != nil {
			return nil, err
		}
	}
	w := &Workspace{MergedDir: req.MergedDir, run: run}
	if err := w.prepareTarget(req, members, run, seam); err != nil {
		return nil, err
	}
	if w.Reused {
		return w, nil
	}
	if err := w.plan(req, members, run, seam); err != nil {
		return nil, err
	}
	if hasBlockMembers(members) {
		members, err = w.mountBlockMembers(members, seam)
		if err != nil {
			return nil, w.fail(err)
		}
		members, err = pruneBySubdir(members, req, run)
		if err != nil {
			return nil, w.fail(err)
		}
	}
	if req.Concat != nil {
		if err := w.mountConcatBase(req, seam); err != nil {
			return nil, err
		}
		base := concatTransit(w.runtimeDir)
		members[len(members)-1].path = filepath.Join(base, req.Concat.ViewName)
	}
	if req.ReadOnly && len(members) == 1 {
		return mountDirect(req, members[0], w, seam)
	}
	return mountOverlay(req, members, w, seam)
}

// underPrefix reports whether dir is prefix itself or lives below
// it. Both sides are cleaned, and the comparison honors the
// directory boundary so a sibling of the prefix never matches.
func underPrefix(dir, prefix string) bool {
	dir, prefix = filepath.Clean(dir), filepath.Clean(prefix)
	if dir == prefix {
		return true
	}
	return strings.HasPrefix(dir, prefix+string(filepath.Separator))
}

// prepareTarget creates the target when missing and resolves the
// injected table: a live entry with matching identity is reused, a
// different identity conflicts, and a listed-but-dead entry is torn
// down before the fresh stack is built. A target that cannot be
// statted is left for the table phase or the final guard to report.
func (w *Workspace) prepareTarget(
	req Request,
	members []member,
	run runFunc,
	seam hooks,
) error {
	if _, err := os.Stat(req.MergedDir); errors.Is(err, os.ErrNotExist) {
		if err := w.mkdirNew(req.MergedDir); err != nil {
			return w.fail(err)
		}
	}
	reused, err := w.tablePhase(req, members, run, seam)
	if err != nil {
		return w.fail(err)
	}
	w.Reused = reused
	return nil
}

// tablePhase resolves the injected snapshot for the target point. A
// nil table skips every table-driven check.
func (w *Workspace) tablePhase(
	req Request,
	members []member,
	run runFunc,
	seam hooks,
) (bool, error) {
	if req.Table == nil {
		return false, nil
	}
	entry, listed := findEntry(req.Table, req.MergedDir)
	if !listed {
		return false, nil
	}
	if !servingOf(seam, req.MergedDir) {
		return false, cleanDeadEntry(entry, run, seam)
	}
	if err := identity(req, members, entry); err != nil {
		return false, err
	}
	if !req.ReadOnly && entry.Upper != "" {
		w.UpperDir = entry.Upper
		w.runtimeDir = filepath.Dir(entry.Upper)
	}
	return true, nil
}

// cleanDeadEntry tears down a listed-but-dead target and the overlay
// lowers it still names under its combination directory. A remount
// into a different session would otherwise leave orphaned squashfuse
// lowers behind, because the new combination directory is a different
// path.
func cleanDeadEntry(entry Entry, run runFunc, seam hooks) error {
	if err := cleanPoint(entry.Point, run, seam); err != nil {
		return err
	}
	runtime := CombinationRuntime(entry)
	if runtime == "" {
		return nil
	}
	for i := len(entry.Lowers) - 1; i >= 0; i-- {
		lower := entry.Lowers[i]
		if lower == runtime || !underPrefix(lower, runtime) {
			continue
		}
		if err := cleanPoint(lower, run, seam); err != nil {
			return err
		}
	}
	return nil
}

// findEntry returns the last table row at the exact point; later
// mounts shadow earlier ones.
func findEntry(table []Entry, point string) (Entry, bool) {
	found := Entry{}
	ok := false
	for _, entry := range table {
		if entry.Point == point {
			found, ok = entry, true
		}
	}
	return found, ok
}

// identity verifies a live entry at the target serves exactly what
// this request would build. A planned subdir makes reuse
// unverifiable: the daemon consumes the option and the mount table
// never shows it, so the engine conflicts instead of guessing.
func identity(req Request, members []member, entry Entry) error {
	if req.Subdir != "" {
		return fmt.Errorf(
			"workspace: %s already mounts %s and the mount table cannot show a subdir; refusing to guess whether it serves %s; unmount it first",
			req.MergedDir, entry.Source, req.Subdir)
	}
	if req.ReadOnly && len(members) == 1 {
		if entry.Upper == "" && len(entry.Lowers) == 0 &&
			entry.Source == members[0].fsname {
			return nil
		}
		return conflictErr(req.MergedDir, entry)
	}
	return overlayIdentity(req, members, entry, !req.ReadOnly)
}

// overlayIdentity accepts a live overlay whose lowers are exactly
// the planned transit points under the request's session-scoped
// combination directory, each transit point currently serving the
// matching stack member's fsname, with an upper for a read-write
// view and none for a read-only chain. The live view masks the
// target's own device and inode, so the runtime directory is read
// from the entry instead of a fresh stat. The image identity check
// reads every planned transit point back from the same injected
// table: a missing row, a non-squashfuse row, or a source that
// differs from the requested member means the merged view was built
// from a different image and must not be reused silently.
func overlayIdentity(req Request, members []member, entry Entry, writable bool) error {
	runtime := ""
	switch {
	case writable && filepath.Base(entry.Upper) == "upper":
		runtime = filepath.Dir(entry.Upper)
	case !writable && entry.Upper == "" && len(entry.Lowers) > 0:
		runtime = filepath.Dir(entry.Lowers[0])
	}
	if runtime == "" {
		return conflictErr(req.MergedDir, entry)
	}
	// A live view already serving this image is reusable even when
	// the caller's combination directory differs — for example an
	// implicit mount that discovers another session's healthy view
	// and discards its own throwaway record. Image identity below
	// is what decides reuse; the caller's RuntimeDir is only for a
	// fresh stack.
	lowers := plannedLowers(runtime, len(members))
	if !slices.Equal(entry.Lowers, lowers) {
		return conflictErr(req.MergedDir, entry)
	}
	liveSources, ok := liveLowerSources(req.Table, lowers)
	if !ok || !slices.Equal(liveSources, memberNames(members)) {
		return conflictErr(req.MergedDir, entry)
	}
	return nil
}

// liveLowerSources reports the fsname each planned transit point
// currently publishes, in the given order. A missing table entry, a
// non-squashfuse row, or an empty source makes the identity
// unverifiable, so ok is false and the caller refuses to reuse
// rather than silently serving a different image.
func liveLowerSources(table []Entry, lowers []string) ([]string, bool) {
	sources := make([]string, 0, len(lowers))
	for _, point := range lowers {
		entry, listed := findEntry(table, point)
		if !listed || !isSquashfuseRow(entry) {
			return nil, false
		}
		sources = append(sources, entry.Source)
	}
	return sources, true
}

// isSquashfuseRow reports whether a table row is a FUSE mount whose
// source is the fsname squashfuse_ll published. The overlay merged
// view itself is a fuse.fuse-overlayfs row, but its source is the
// literal "overlay" and never a member fsname, so transit points
// additionally require a non-empty source that does not collide
// with that sentinel.
func isSquashfuseRow(entry Entry) bool {
	if entry.Source == "" || entry.Source == "overlay" {
		return false
	}
	return entry.FSType == "fuse" || strings.HasPrefix(entry.FSType, "fuse.")
}

func conflictErr(point string, entry Entry) error {
	return fmt.Errorf(
		"workspace: %s already mounts a different view (source %q, upper %q); "+
			"refusing to restack it silently; unmount it first to serve a different image",
		point, entry.Source, entry.Upper)
}

// plan guards the target, then adopts the caller-supplied
// combination directory and clears any residue on the planned
// transit points. The guard runs first so every refusal leaves the
// system untouched. A read-only single member mounts directly and
// needs no runtime planning — unless a concat source is set, whose
// transit point always lives under the runtime directory.
func (w *Workspace) plan(
	req Request,
	members []member,
	run runFunc,
	seam hooks,
) error {
	if err := guardTarget(req.MergedDir); err != nil {
		return w.fail(err)
	}
	if req.ReadOnly && len(members) == 1 && req.Concat == nil && !hasBlockMembers(members) && !hasBlockTransits(req) {
		return nil
	}
	w.runtimeDir = filepath.Clean(req.RuntimeDir)
	if !req.ReadOnly {
		w.UpperDir = filepath.Join(w.runtimeDir, "upper")
	}
	// A prune window an earlier interrupted mount left behind would
	// mask the point the next window mounts on. The lowercheck
	// overlay is a dependent of the lowers: while it is live it
	// holds every lowerdir busy, so it is torn down first and the
	// lowers unmount cleanly instead of escalating to a lazy
	// detach. The concat transit backs the base layer's image
	// file, the opposite dependency direction, and is cleaned
	// after the lowers instead.
	if err := cleanTransit(lowerCheckPoint(w.runtimeDir), req, run, seam); err != nil {
		return w.fail(err)
	}
	if err := cleanRebaseTransits(req, run, seam); err != nil {
		return w.fail(err)
	}
	if err := w.cleanLowers(req, len(members), run, seam); err != nil {
		return w.fail(err)
	}
	if err := cleanBlockTransits(req, run, seam); err != nil {
		return w.fail(err)
	}
	if req.Concat != nil {
		if err := cleanTransit(concatTransit(w.runtimeDir), req, run, seam); err != nil {
			return w.fail(err)
		}
	}
	return nil
}

// concatTransit is the transit point under one runtime directory
// where concatfs exposes the concatenated view of a block payload.
func concatTransit(runtime string) string {
	return filepath.Join(runtime, "concat")
}

// lowerCheckPoint is the transit point under one runtime directory
// where the prune window mounts the read-only merge of the lower
// stack.
func lowerCheckPoint(runtime string) string {
	return filepath.Join(runtime, "lowercheck")
}

// cleanTransit tears down a stale transit point an earlier mount
// under the same runtime directory left behind, so a replay starts
// from one consistent combination. A point that already reads as a
// plain directory cleans up as a no-op.
func cleanTransit(point string, req Request, run runFunc, seam hooks) error {
	if _, listed := findEntry(req.Table, point); !listed && !liveOf(seam, point) {
		return nil
	}
	return cleanPoint(point, run, seam)
}

// mountConcatBase creates the concat transit point under the runtime
// directory and starts concatfs on the block directory. The transit
// point joins the teardown list as soon as the exec succeeds, and
// the poll confirms the virtual view is up before the base layer
// consumes it. Being the first recorded mount, the transit point is
// torn down last.
func (w *Workspace) mountConcatBase(req Request, seam hooks) error {
	if err := w.mkdirNew(req.RuntimeDir); err != nil {
		return w.fail(err)
	}
	if err := w.mkdirNew(w.runtimeDir); err != nil {
		return w.fail(err)
	}
	transit := concatTransit(w.runtimeDir)
	if err := w.mkdirNew(transit); err != nil {
		return w.fail(err)
	}
	if err := w.run("concatfs", req.Concat.BlocksDir, transit); err != nil {
		return w.fail(fmt.Errorf("workspace: %w", err))
	}
	w.mounts = append(w.mounts, transit)
	if err := waitLive(transit, seam); err != nil {
		return w.fail(err)
	}
	return nil
}

// plannedLowers are the lower-N transit points a stack of count
// members uses under one runtime directory, newest first.
func plannedLowers(runtime string, count int) []string {
	points := make([]string, 0, count)
	for i := 0; i < count; i++ {
		points = append(points, filepath.Join(runtime, fmt.Sprintf("lower-%d", i)))
	}
	return points
}

// cleanLowers tears down any mount a planned transit point still
// carries, found through the table or the live probe, so the fresh
// stack becomes the one consistent combination. A point that
// already reads as a plain directory cleans up as a no-op.
func (w *Workspace) cleanLowers(
	req Request,
	count int,
	run runFunc,
	seam hooks,
) error {
	for _, point := range plannedLowers(w.runtimeDir, count) {
		if _, listed := findEntry(req.Table, point); !listed && !liveOf(seam, point) {
			continue
		}
		if err := cleanPoint(point, run, seam); err != nil {
			return err
		}
	}
	return nil
}

// cleanPoint tears one point down: fusermount3 -u, escalating to the
// lazy -uz, then waiting until the point reads as a plain directory
// again.
func cleanPoint(point string, run runFunc, seam hooks) error {
	if pointGone(point, seam) {
		return nil
	}
	if err := run("fusermount3", "-u", point); err != nil {
		if lazyErr := run("fusermount3", "-uz", point); lazyErr != nil {
			return fmt.Errorf("workspace: cannot unmount %s: %w (lazy: %v)",
				point, err, lazyErr)
		}
	}
	return waitGone(point, seam)
}

// pointGone reports the point is a plain directory again, or is
// absent, so nothing can be mounted on it.
func pointGone(point string, seam hooks) bool {
	if _, err := os.Stat(point); err != nil {
		return errors.Is(err, os.ErrNotExist)
	}
	return !liveOf(seam, point)
}

// waitGone polls until a torn-down point stops behaving as a mount.
func waitGone(point string, seam hooks) error {
	sleep := sleepOf(seam)
	for i := 0; i < livePolls; i++ {
		if pointGone(point, seam) {
			return nil
		}
		sleep(liveInterval)
	}
	return fmt.Errorf("workspace: %s is still mounted after unmounting", point)
}

// guardTarget requires the mount point to be an empty directory
// owned by the current user, so a mount never hides user data and
// FUSE ownership stays consistent.
func guardTarget(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("workspace: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("workspace: %s is not a directory", dir)
	}
	if err := checkOwner(fileUID(info), uint32(os.Geteuid())); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("workspace: %w", err)
	}
	if len(entries) > 0 {
		return fmt.Errorf("workspace: %s is not empty; refusing to hide its content under a mount", dir)
	}
	return nil
}

// checkOwner compares a mount point owner with the effective uid.
// FUSE restricts unmounting to the mount owner, so a point owned by
// someone else is refused up front. Pure for testability.
func checkOwner(uid, euid uint32) error {
	if uid != euid {
		return fmt.Errorf("workspace: mount point owner %d is not the current user %d",
			uid, euid)
	}
	return nil
}

// CheckSubdir validates the image-relative subdir shape before any
// tool runs: a clean relative slash path with no empty, ".", or ".."
// segment, so the mount root can never escape the image. The command
// layer maps a refusal to a usage error.
func CheckSubdir(subdir string) error {
	if subdir == "" {
		return nil
	}
	if path.IsAbs(subdir) || subdir != path.Clean(subdir) || hasDotSegment(subdir) {
		return fmt.Errorf("workspace: subdir %q must be a clean relative image path", subdir)
	}
	return nil
}

// hasDotSegment reports an empty, ".", or ".." segment. A cleaned
// relative path only carries them when it climbs above the root.
func hasDotSegment(subdir string) bool {
	for _, seg := range strings.Split(subdir, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return true
		}
	}
	return false
}

// pruneBySubdir pre-checks every layer for the requested subdir and
// drops the layers that do not contain it: a layer that never
// touched the subtree contributes nothing to the subtree view, and
// every surviving layer mounts with the same subdir option so the
// stack paths align. A missing subdir inside an image fails with an
// obscure tool error once mounted, so the check runs before any
// mount command; when no layer has the subdir nothing was created
// and the error names the subdir and every image.
func pruneBySubdir(members []member, req Request, run runFunc) ([]member, error) {
	if req.Subdir == "" {
		return members, nil
	}
	var kept []member
	for _, m := range members {
		ok, err := memberHasSubdir(m, req.Subdir, run)
		if err != nil {
			return nil, err
		}
		if ok {
			kept = append(kept, m)
		}
	}
	if len(kept) == 0 {
		return nil, fmt.Errorf(
			"workspace: subdir %s is missing from every stack member (%s)",
			req.Subdir, strings.Join(memberNames(members), ", "))
	}
	return kept, nil
}

// memberHasSubdir asks the layer's container whether subdir exists.
// A probe error is a mount failure. Absence drops the layer.
func memberHasSubdir(m member, subdir string, run runFunc) (bool, error) {
	format, err := container.Lookup(m.format)
	if err != nil {
		return false, fmt.Errorf("workspace: %w", err)
	}
	ok, err := format.HasSubdir(toolRunner(run), m.path, subdir)
	if err != nil {
		return false, fmt.Errorf("workspace: subdir %s: %w", subdir, err)
	}
	return ok, nil
}

// toolRunner adapts the mount runner to a container ToolRunner.
// Subdir probes pass a nil environment, which this adapter drops.
func toolRunner(run runFunc) container.ToolRunner {
	return func(name string, _ []string, args ...string) error {
		return run(name, args...)
	}
}

// memberNames lists the fsname each stack member publishes, in
// mount order (newest increment first, base image last). The
// identity check compares this sequence against the live transit
// points; error messages join it for display.
func memberNames(members []member) []string {
	names := make([]string, 0, len(members))
	for _, m := range members {
		names = append(names, m.fsname)
	}
	return names
}

// preflight checks the FUSE device and every tool this request
// needs, naming a missing prerequisite so callers can point at the
// doctor command.
func preflight(req Request) error {
	if err := available(); err != nil {
		return err
	}
	tools, err := requiredTools(req)
	if err != nil {
		return err
	}
	return lookTools(tools...)
}

// requiredTools names the binaries this request will exec. The FUSE
// client comes from the container. A squashfs subdir probe also
// needs unsquashfs; erofs probes with the client already listed.
// The writable upper stays on the shared fuse-overlayfs.
func requiredTools(req Request) ([]string, error) {
	members, err := stack(req)
	if err != nil {
		return nil, err
	}
	var tools []string
	for _, m := range members {
		if m.format == "" {
			return nil, errors.New("workspace: container format is required")
		}
		format, err := container.Lookup(m.format)
		if err != nil {
			return nil, fmt.Errorf("workspace: %w", err)
		}
		client, err := fuseClient(format)
		if err != nil {
			return nil, err
		}
		for _, tool := range []string{client, subdirProbe(format, req.Subdir)} {
			if tool != "" && !slices.Contains(tools, tool) {
				tools = append(tools, tool)
			}
		}
	}
	tools = append(tools, "fusermount3")
	if !req.ReadOnly || len(members) > 1 {
		tools = append(tools, "fuse-overlayfs")
	}
	if req.Concat != nil {
		tools = append(tools, "concatfs")
	}
	if hasBlockMembers(members) && !slices.Contains(tools, "concatfs") {
		tools = append(tools, "concatfs")
	}
	return tools, nil
}

// fuseClient is the program MountArgv will exec for one lower.
func fuseClient(format container.Format) (string, error) {
	argv := format.MountArgv("", "", "", "", "")
	if len(argv) == 0 || argv[0] == "" {
		return "", fmt.Errorf("workspace: %s has no mount command", format.Type())
	}
	return argv[0], nil
}

// subdirProbe is the extra binary a subdir check execs before the
// mount. Squashfs asks unsquashfs. Erofs reuses its FUSE client.
func subdirProbe(format container.Format, subdir string) string {
	if subdir == "" || format.Type() != "squashfs" {
		return ""
	}
	return "unsquashfs"
}

// lookTools names the first missing tool as unavailable.
func lookTools(tools ...string) error {
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			return fmt.Errorf("%w: %s is missing", ErrUnavailable, tool)
		}
	}
	return nil
}

// stack normalizes the request into mount order: newest increment
// first and the full image last, matching the lower-N numbering.
func stack(req Request) ([]member, error) {
	if len(req.Members) > 0 {
		if req.Concat != nil {
			return nil, errors.New("workspace: explicit members require materialized payloads")
		}
		for _, m := range req.Members {
			if m.Path == "" {
				return nil, errors.New("workspace: member payload path is required")
			}
		}
		req = memberRequest(req)
	}
	if depth := 1 + len(req.Layers); depth > maxStackDepth {
		return nil, fmt.Errorf(
			"workspace: stack holds %d members; the limit is %d",
			depth, maxStackDepth)
	}
	members := make([]member, 0, len(req.Layers)+1)
	for i := len(req.Layers) - 1; i >= 0; i-- {
		format := req.Format
		if len(req.Members) > 0 {
			format = req.Members[i+1].Format
		}
		members = append(members, member{
			path:   req.Layers[i],
			fsname: layerFSName(req, i),
			format: format,
		})
		if len(req.Members) > 0 {
			members[len(members)-1].blocks = req.Members[i+1].Blocks
		}
	}
	fsname := req.FullFSName
	if fsname == "" {
		fsname = req.Full
	}
	base := member{path: req.Full, fsname: fsname, format: req.Format}
	if len(req.Members) > 0 {
		base.blocks = req.Members[0].Blocks
	}
	return append(members, base), nil
}

// memberRequest projects explicit members onto the legacy runtime path fields.
func memberRequest(req Request) Request {
	if len(req.Members) == 0 {
		return req
	}
	base := req.Members[0]
	req.Full, req.FullFSName, req.Format = base.Path, base.FSName, base.Format
	req.Layers, req.LayerFSNames = nil, nil
	for _, m := range req.Members[1:] {
		req.Layers = append(req.Layers, m.Path)
		req.LayerFSNames = append(req.LayerFSNames, m.FSName)
	}
	return req
}

// layerFSName is the identity one layer publishes: its override when
// the mounted bytes are a cache copy, its path otherwise.
func layerFSName(req Request, i int) string {
	if i < len(req.LayerFSNames) && req.LayerFSNames[i] != "" {
		return req.LayerFSNames[i]
	}
	return req.Layers[i]
}

// mountDirect mounts one read-only member straight onto the target
// directory: one FUSE daemon, no runtime directory, and no overlay.
func mountDirect(req Request, m member, w *Workspace, seam hooks) (*Workspace, error) {
	if err := w.mountPoint(m, req, req.MergedDir, seam); err != nil {
		return nil, err
	}
	return w, nil
}

// mountOverlay mounts every member under the runtime directory and
// merges them onto the target with fuse-overlayfs. A read-write view
// keeps an upper and a work directory; a read-only chain merges
// lowerdir-only. A read-write request that asks for an upper prune
// over a reused non-empty upper runs the prune window between the
// lowers and the merge.
func mountOverlay(req Request, members []member, w *Workspace, seam hooks) (*Workspace, error) {
	if err := w.mkdirNew(req.RuntimeDir); err != nil {
		return nil, w.fail(err)
	}
	// The prune decision reads the upper before prepareRuntime may
	// create it: only a reused upper that already carries content
	// can hold entries the lower stack has baked in.
	wantPrune := !req.ReadOnly && req.PruneUpper && dirHasContent(w.UpperDir)
	if err := w.prepareRuntime(req.ReadOnly); err != nil {
		return nil, w.fail(err)
	}
	points := plannedLowers(w.runtimeDir, len(members))
	for i, m := range members {
		if err := w.mkdirNew(points[i]); err != nil {
			return nil, w.fail(err)
		}
		if err := w.mountPoint(m, req, points[i], seam); err != nil {
			return nil, err
		}
	}
	if req.Rebase != nil {
		if req.ReadOnly {
			return nil, w.fail(errors.New("workspace: a read-only view cannot rebase an upper"))
		}
		if err := w.rebaseUpper(req, points, seam); err != nil {
			return nil, w.fail(fmt.Errorf("workspace: upper rebase: %w", err))
		}
	}
	if wantPrune {
		w.pruneWindow(req, points, seam)
	}
	if req.Rebase != nil {
		plan, err := pendingRebasePlan(w.runtimeDir, req.Rebase.Identity)
		if err != nil || plan == nil {
			return nil, w.fail(errors.Join(errors.New("workspace: missing upper rebase plan"), err))
		}
		plan.Ready = true
		if err := saveRebasePlan(w.runtimeDir, *plan); err != nil {
			return nil, w.fail(err)
		}
	}
	if err := w.mountMerged(req, points, seam); err != nil {
		return nil, err
	}
	return w, nil
}

// dirHasContent reports whether dir exists and holds at least one
// entry. A missing or unreadable directory reads as empty.
func dirHasContent(dir string) bool {
	f, err := os.Open(dir)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	entries, err := f.ReadDir(1)
	return err == nil && len(entries) > 0
}

// pruneWindow runs the upper prune in the only safe slot of the
// mount flow: every lower is mounted and the merged view is not up
// yet, so the upper belongs to no live view. Every failure degrades
// to a stderr warning: mounting with an unpruned upper is always
// safe, so the window never blocks the mount. The temporary point
// never joins the w.mounts terminal accounting; a residue is torn
// down by the next mount's plan phase.
func (w *Workspace) pruneWindow(req Request, lowers []string, seam hooks) {
	// The start line precedes the lowercheck mount and every
	// deletion. done is written only after the window returns
	// success, which is still before mountMerged.
	prog := newPruneProgress(req.Progress)
	prog.start()
	report, err := w.runPruneWindow(req, lowers, seam, prog)
	if report != nil {
		w.Prune = report
	}
	if err != nil {
		// The mount continues. Callers that asked to prune a
		// non-empty upper keep the served pins when this is set.
		w.PruneFailed = true
		prog.abandon()
		// Every window failure already carries its own context and
		// the "workspace: " package tag; warnf only adds the
		// warning prefix, so nothing is prefixed twice.
		warnf("%v", err)
		return
	}
	prog.succeed()
}

// runPruneWindow mounts the read-only lowerdir-only merge at the
// lowercheck point, prunes the upper against it, and tears the
// point down with fusermount3 -u plus a wait until it is gone. It
// reports the prune report and the joined window failures. The
// deferred removal leaves no residue whenever the point ends as a
// plain directory, on the success path and on every failure path.
// A teardown failure counts as an error: a live residue would only
// be cleaned by the next mount's plan phase.
func (w *Workspace) runPruneWindow(
	req Request,
	lowers []string,
	seam hooks,
	prog *pruneProgress,
) (*PruneReport, error) {
	point := lowerCheckPoint(w.runtimeDir)
	if err := w.mkdirNew(point); err != nil {
		return nil, err
	}
	defer func() {
		if pointGone(point, seam) {
			_ = os.Remove(point)
		}
	}()
	argv := overlayArgv(lowers, "", "", req.FuseOptions, point)
	if err := w.run(argv[0], argv[1:]...); err != nil {
		// The runner error names the tool but not the phase, so the
		// prune warning tags it the way PruneUpper tags its own
		// root errors.
		return nil, fmt.Errorf("workspace: upper prune: %w", err)
	}
	if err := waitLive(point, seam); err != nil {
		// The daemon may still come up; never leave the point behind.
		return nil, errors.Join(err, cleanPoint(point, w.run, seam))
	}
	report, pruneErr := pruneOf(seam, prog)(w.UpperDir, point)
	return &report, errors.Join(pruneErr, cleanPoint(point, w.run, seam))
}

// pruneOf returns the prune seam, defaulting to the real PruneUpper.
// The default walk reports entry progress; a test seam replaces the
// whole walk and therefore owns its own progress.
func pruneOf(seam hooks, prog *pruneProgress) func(upperDir, lowerView string) (PruneReport, error) {
	if seam.prune != nil {
		return seam.prune
	}
	return func(upperDir, lowerView string) (PruneReport, error) {
		return pruneWithProgress(upperDir, lowerView, pruneHooks{}, prog)
	}
}

// warnf reports one non-fatal problem on stderr with the same bare
// "warning: " prefix the cli layer uses: the errors callers pass in
// already carry the "workspace: " package tag and their own context.
// It is a variable so tests can capture the warnings.
var warnf = func(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, "warning: "+format+"\n", args...)
}

// prepareRuntime creates the runtime directory of this mount, plus
// the upper and work directories of a read-write view. An existing
// upper is reused as it is, so a remount keeps earlier changes.
func (w *Workspace) prepareRuntime(readOnly bool) error {
	if err := w.mkdirNew(w.runtimeDir); err != nil {
		return err
	}
	if readOnly {
		return nil
	}
	if err := w.mkdirNew(w.UpperDir); err != nil {
		return err
	}
	return w.mkdirNew(filepath.Join(w.runtimeDir, "work"))
}

// mountPoint runs the layer's FUSE client and waits for the point
// to become a live mount. The point joins the teardown list as soon
// as the exec succeeds, so a mount that never comes up is still
// unmounted best effort.
func (w *Workspace) mountPoint(m member, req Request, point string, seam hooks) error {
	argv, err := layerArgv(m, req, point)
	if err != nil {
		return w.fail(err)
	}
	if err := w.run(argv[0], argv[1:]...); err != nil {
		return w.fail(fmt.Errorf("workspace: %w", err))
	}
	w.mounts = append(w.mounts, point)
	if err := waitLive(point, seam); err != nil {
		return w.fail(err)
	}
	return nil
}

// mountMerged merges the mounted lowers onto the target. The lowers
// are the member transit points only: the concat base transit point
// joins w.mounts for teardown but is never an overlay lower. A
// read-write view carries upperdir and workdir; a read-only chain
// stays lowerdir-only.
func (w *Workspace) mountMerged(req Request, lowers []string, seam hooks) error {
	upper, work := "", ""
	if !req.ReadOnly {
		upper = w.UpperDir
		work = filepath.Join(w.runtimeDir, "work")
	}
	argv := overlayArgv(lowers, upper, work, req.FuseOptions, req.MergedDir)
	if err := w.run(argv[0], argv[1:]...); err != nil {
		return w.fail(fmt.Errorf("workspace: %w", err))
	}
	w.mounts = append(w.mounts, req.MergedDir)
	if err := waitLive(req.MergedDir, seam); err != nil {
		return w.fail(err)
	}
	return nil
}

// layerArgv is the FUSE command for one layer. fsname keeps the
// original image path in the mount table even when the mounted
// bytes are a cache copy.
func layerArgv(m member, req Request, point string) ([]string, error) {
	format, err := container.Lookup(m.format)
	if err != nil {
		return nil, fmt.Errorf("workspace: %w", err)
	}
	argv := format.MountArgv(m.fsname, req.Subdir, req.SquashfsOptions, m.path, point)
	if len(argv) == 0 {
		return nil, fmt.Errorf("workspace: %s has no mount command", m.format)
	}
	return argv, nil
}

// overlayArgv builds the fuse-overlayfs command line. An empty upper
// merges lowerdir-only for a read-only chain; the configured options
// come last.
func overlayArgv(lowers []string, upper, work, options, merged string) []string {
	opts := "lowerdir=" + strings.Join(lowers, ":")
	if upper != "" {
		opts += ",upperdir=" + upper + ",workdir=" + work
	}
	if options != "" {
		opts += "," + options
	}
	return []string{"fuse-overlayfs", "-o", opts, merged}
}

// waitLive polls until the point behaves as a live mount. Both FUSE
// tools daemonize, so a successful exec does not yet mean the mount
// exists; a mount that never appears or dies again fails here.
func waitLive(point string, seam hooks) error {
	sleep := sleepOf(seam)
	for i := 0; i < livePolls; i++ {
		if liveOf(seam, point) {
			return nil
		}
		sleep(liveInterval)
	}
	return fmt.Errorf("workspace: %s did not become a live mount", point)
}

// liveOf probes through the seam, defaulting to the real stat probe.
func liveOf(seam hooks, point string) bool {
	if seam.live != nil {
		return seam.live(point)
	}
	return liveMount(point)
}

// servingOf probes through the seam, defaulting to the real
// attachment and connection probes. A killed daemon can leave the
// mount attached with the kernel attribute cache still answering
// stat, so the reuse gate needs the stronger signal: only a mount
// whose connection still serves requests may be reused, and a
// listed-but-dead point takes the teardown path. A fake kernel that
// models only attached or detached points treats attached as
// serving.
func servingOf(seam hooks, point string) bool {
	switch {
	case seam.serving != nil:
		return seam.serving(point)
	case seam.live != nil:
		return seam.live(point)
	default:
		return liveMount(point) && answers(point)
	}
}

// sleepOf is the seam pause between polls, defaulting to the real
// clock.
func sleepOf(seam hooks) func(time.Duration) {
	if seam.sleep != nil {
		return seam.sleep
	}
	return time.Sleep
}

// mkdirNew creates dir when it does not exist and records every new
// directory, so a failed mount removes all of them. Existing
// directories are never recorded and never removed.
func (w *Workspace) mkdirNew(dir string) error {
	fresh, err := missingChain(dir)
	if err != nil {
		return fmt.Errorf("workspace: %w", err)
	}
	if len(fresh) == 0 {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("workspace: %w", err)
	}
	w.created = append(w.created, fresh...)
	return nil
}

// missingChain lists the directories MkdirAll would create for dir,
// from the topmost missing ancestor down to dir. An empty result
// means dir already exists.
func missingChain(dir string) ([]string, error) {
	var chain []string
	for d := dir; ; {
		_, err := os.Stat(d)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		chain = append(chain, d)
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	slices.Reverse(chain)
	return chain, nil
}

// fail tears down everything this call created and reports the
// primary error, appending cleanup residue instead of masking the
// cause.
func (w *Workspace) fail(cause error) error {
	if err := w.cleanup(); err != nil {
		return fmt.Errorf("%w (cleanup: %v)", cause, err)
	}
	return cause
}

// cleanup unmounts in reverse order and then removes the directories
// this call created, deepest first. Removal takes only empty
// directories, so no user data is ever deleted.
func (w *Workspace) cleanup() error {
	var errs []error
	if err := w.Unmount(); err != nil {
		errs = append(errs, err)
	}
	for i := len(w.created) - 1; i >= 0; i-- {
		if err := os.Remove(w.created[i]); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	w.created = nil
	return errors.Join(errs...)
}

// Unmount detaches every mount in reverse order. A disk image uses
// hdiutil detach with no -force and stays mounted when detach fails
// or info still lists the mountpoint. FUSE views use fusermount3.
func (w *Workspace) Unmount() error {
	if w.dmg {
		return w.detachDMG()
	}
	var errs []error
	for i := len(w.mounts) - 1; i >= 0; i-- {
		if err := w.run("fusermount3", "-u", w.mounts[i]); err != nil {
			errs = append(errs, err)
		}
	}
	w.mounts = nil
	return errors.Join(errs...)
}

// SmokeReadOnly mounts one squashfs file read-only, lists its root,
// and unmounts it again. Missing tools and a missing /dev/fuse
// return ErrUnavailable so callers can skip; a file that does not
// mount or read returns a real error.
func SmokeReadOnly(imagePath string) error {
	return smokeReadOnly(imagePath, runTool)
}

func smokeReadOnly(imagePath string, run runFunc) error {
	if err := available(); err != nil {
		return err
	}
	if err := lookTools("squashfuse_ll", "fusermount3"); err != nil {
		return err
	}
	point, err := os.MkdirTemp("", "tiler-smoke-")
	if err != nil {
		return fmt.Errorf("workspace: %w", err)
	}
	defer func() { _ = os.RemoveAll(point) }()
	if err := run("squashfuse_ll", imagePath, point); err != nil {
		return fmt.Errorf("workspace: smoke mount: %w", err)
	}
	_, listErr := os.ReadDir(point)
	unmountErr := run("fusermount3", "-u", point)
	if listErr != nil {
		return fmt.Errorf("workspace: smoke read: %w", listErr)
	}
	if unmountErr != nil {
		return fmt.Errorf("workspace: smoke unmount: %w", unmountErr)
	}
	return nil
}

// runTool is the default runner. A non-zero exit carries the
// trimmed combined output.
func runTool(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", name, err, bytes.TrimSpace(out))
	}
	return nil
}
