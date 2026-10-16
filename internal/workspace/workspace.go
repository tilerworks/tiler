// Package workspace mounts squashfs images and their incremental
// chains as read-only or writable merged views. It serves both the
// incremental build quality gate and the formal mount flow: no cobra
// command is registered here, and no session record is written.
package workspace

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jiangxin/tiler/internal/config"
)

// ErrUnavailable means mounting cannot work here: the OS is not
// Linux, or /dev/fuse and the FUSE tools cannot be used. Callers
// treat it as a skip.
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
	// ReadOnly mounts without an upper: a single member goes
	// straight onto MergedDir, and a chain merges lowerdir-only.
	ReadOnly bool
	// Subdir is an image-relative prefix exposed as the mount
	// root. Empty mounts whole images.
	Subdir string
	// Table is an injected mount table snapshot. Nil skips the
	// table-driven checks.
	Table []Entry
	// SquashfsOptions is appended to the squashfuse_ll options
	// after the built-in ones.
	SquashfsOptions string
	// FuseOptions is appended to the fuse-overlayfs options after
	// the built-in ones.
	FuseOptions string
	// RuntimeDir is the configured runtime directory. Read-only
	// points and the upper and work directories live under it.
	RuntimeDir string
	// MergedDir is where the merged view is mounted.
	MergedDir string
	// Run executes external tools. Nil runs them through exec.
	Run func(name string, args ...string) error
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

	runtimeDir string
	mounts     []string
	created    []string
	run        runFunc
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
}

// member is one squashfs of the stack: the bytes to mount and the
// identity published in the mount table.
type member struct {
	path   string
	fsname string
}

// Mount mounts the requested view.
//
// Each squashfs member is mounted read-only with squashfuse_ll,
// newest increment first and the full image last, passing the
// original image path as the FUSE source name so the mount table can
// identify the image even when the mounted bytes are a cache copy.
// A read-write view merges the stack with fuse-overlayfs over an
// upper and a work directory under runtime/up-<dev>-<ino>/; a
// read-only chain merges lowerdir-only, and a read-only single image
// mounts straight onto the target with no runtime state. When Subdir
// is set, every member is pre-checked with unsquashfs first and the
// members missing the subdirectory are dropped before any mount.
//
// Both FUSE tools daemonize, so every exec is followed by a poll
// that confirms the point is a live mount. A failing or vanished
// mount tears down every point and directory this call created and
// reports the primary error. Non-Linux hosts, a missing /dev/fuse,
// and missing tools return ErrUnavailable.
func Mount(req Request) (*Workspace, error) {
	run := req.Run
	if run == nil {
		run = runTool
	}
	return mount(req, run, hooks{})
}

func mount(req Request, run runFunc, seam hooks) (*Workspace, error) {
	if err := preflight(req); err != nil {
		return nil, err
	}
	if req.Full == "" || req.RuntimeDir == "" || req.MergedDir == "" {
		return nil, errors.New("workspace: full image, runtime dir, and merged dir are required")
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
	members, err = pruneBySubdir(members, req, run)
	if err != nil {
		return nil, err
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
		return false, cleanPoint(req.MergedDir, run, seam)
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
	return overlayIdentity(req, entry, len(members), !req.ReadOnly)
}

// overlayIdentity accepts a live overlay whose lowers are exactly
// the planned transit points under one runtime directory of the
// configured runtime root, with an upper for a read-write view and
// none for a read-only chain. The live view masks the target's own
// device and inode, so the runtime directory is read from the entry
// instead of a fresh stat.
func overlayIdentity(req Request, entry Entry, count int, writable bool) error {
	runtime := ""
	switch {
	case writable && filepath.Base(entry.Upper) == "upper":
		runtime = filepath.Dir(entry.Upper)
	case !writable && entry.Upper == "" && len(entry.Lowers) > 0:
		runtime = filepath.Dir(entry.Lowers[0])
	}
	if runtime == "" || !strings.HasPrefix(filepath.Base(runtime), "up-") ||
		!underPrefix(runtime, req.RuntimeDir) {
		return conflictErr(req.MergedDir, entry)
	}
	if !slices.Equal(entry.Lowers, plannedLowers(runtime, count)) {
		return conflictErr(req.MergedDir, entry)
	}
	return nil
}

func conflictErr(point string, entry Entry) error {
	return fmt.Errorf(
		"workspace: %s already mounts a different view (source %q, upper %q); refusing to restack it silently",
		point, entry.Source, entry.Upper)
}

// plan guards the target, then derives the runtime directory of a
// fresh stack and clears any residue on the planned transit points.
// The guard runs first so every refusal leaves the system
// untouched. A read-only single member mounts directly and needs no
// runtime planning.
func (w *Workspace) plan(
	req Request,
	members []member,
	run runFunc,
	seam hooks,
) error {
	if err := guardTarget(req.MergedDir); err != nil {
		return w.fail(err)
	}
	if req.ReadOnly && len(members) == 1 {
		return nil
	}
	dev, ino, err := devIno(req.MergedDir)
	if err != nil {
		return w.fail(fmt.Errorf("workspace: %w", err))
	}
	w.runtimeDir = filepath.Join(req.RuntimeDir, config.RuntimeDirName(dev, ino))
	if !req.ReadOnly {
		w.UpperDir = filepath.Join(w.runtimeDir, "upper")
	}
	if err := w.cleanLowers(req, len(members), run, seam); err != nil {
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

// pruneBySubdir pre-checks every member for the requested subdir and
// drops the members that do not contain it: a layer that never
// touched the subtree contributes nothing to the subtree view, and
// every surviving member mounts with the same subdir option so the
// stack paths align. A missing subdir inside an image fails with an
// obscure tool error once mounted, so the check runs before any
// mount command; when no member has the subdir nothing was created
// and the error names the subdir and every image.
func pruneBySubdir(members []member, req Request, run runFunc) ([]member, error) {
	if req.Subdir == "" {
		return members, nil
	}
	var kept []member
	for _, m := range members {
		// The exit status is the answer; the listing is discarded.
		// -match aborts when the path resolves to nothing and keeps
		// names literal; plain -ls lists the closest existing
		// prefix and exits zero even when the subdir is absent.
		if run("unsquashfs", "-ls", "-match", m.path, req.Subdir) == nil {
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

// memberNames lists the published identities for an error message.
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
	tools := []string{"squashfuse_ll", "fusermount3"}
	if !req.ReadOnly || len(req.Layers) > 0 {
		tools = append(tools, "fuse-overlayfs")
	}
	if req.Subdir != "" {
		tools = append(tools, "unsquashfs")
	}
	return lookTools(tools...)
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
	if depth := 1 + len(req.Layers); depth > maxStackDepth {
		return nil, fmt.Errorf(
			"workspace: stack holds %d members; the limit is %d",
			depth, maxStackDepth)
	}
	members := make([]member, 0, len(req.Layers)+1)
	for i := len(req.Layers) - 1; i >= 0; i-- {
		members = append(members, member{
			path:   req.Layers[i],
			fsname: layerFSName(req, i),
		})
	}
	fsname := req.FullFSName
	if fsname == "" {
		fsname = req.Full
	}
	return append(members, member{path: req.Full, fsname: fsname}), nil
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
// lowerdir-only.
func mountOverlay(req Request, members []member, w *Workspace, seam hooks) (*Workspace, error) {
	if err := w.mkdirNew(req.RuntimeDir); err != nil {
		return nil, w.fail(err)
	}
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
	if err := w.mountMerged(req, seam); err != nil {
		return nil, err
	}
	return w, nil
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

// mountPoint runs squashfuse_ll for one member and waits for the
// point to become a live mount. The point joins the teardown list as
// soon as the exec succeeds, so a mount that never comes up is still
// unmounted best effort.
func (w *Workspace) mountPoint(m member, req Request, point string, seam hooks) error {
	argv := squashfsArgv(m.fsname, req.Subdir, req.SquashfsOptions, m.path, point)
	if err := w.run(argv[0], argv[1:]...); err != nil {
		return w.fail(fmt.Errorf("workspace: %w", err))
	}
	w.mounts = append(w.mounts, point)
	if err := waitLive(point, seam); err != nil {
		return w.fail(err)
	}
	return nil
}

// mountMerged merges the mounted lowers onto the target. A
// read-write view carries upperdir and workdir; a read-only chain
// stays lowerdir-only.
func (w *Workspace) mountMerged(req Request, seam hooks) error {
	upper, work := "", ""
	if !req.ReadOnly {
		upper = w.UpperDir
		work = filepath.Join(w.runtimeDir, "work")
	}
	argv := overlayArgv(w.mounts, upper, work, req.FuseOptions, req.MergedDir)
	if err := w.run(argv[0], argv[1:]...); err != nil {
		return w.fail(fmt.Errorf("workspace: %w", err))
	}
	w.mounts = append(w.mounts, req.MergedDir)
	if err := waitLive(req.MergedDir, seam); err != nil {
		return w.fail(err)
	}
	return nil
}

// squashfsArgv builds the squashfuse_ll command line for one member.
// fsname keeps the original image path in the mount table even when
// the mounted bytes are a cache copy; subdir and the configured
// options are appended after the built-in ones.
func squashfsArgv(fsname, subdir, options, path, point string) []string {
	opts := "fsname=" + fsname
	if subdir != "" {
		opts += ",subdir=" + subdir
	}
	if options != "" {
		opts += "," + options
	}
	return []string{"squashfuse_ll", "-o", opts, path, point}
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

// Unmount detaches every mount in reverse order with fusermount3.
func (w *Workspace) Unmount() error {
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
