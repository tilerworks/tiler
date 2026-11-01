package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The overlay markers the Linux prune probes recognize.
const (
	xattrOpaque     = "trusted.overlay.opaque"
	xattrMetacopy   = "trusted.overlay.metacopy"
	xattrRedirect   = "trusted.overlay.redirect"
	xattrFuseOpaque = "user.fuseoverlayfs.opaque"
)

// platformStat carries the raw stat fields the prune rules need
// beyond os.FileInfo. Only the Linux helpers fill them; on every
// other platform the probes fail and the rules keep all entries.
type platformStat struct {
	// rdev is the device a character device node references. An
	// overlay whiteout is a character device with rdev == 0.
	rdev uint64
	// nlink is the hard link count. Entries with more than one
	// link are kept fail-safe.
	nlink uint64
	// uid and gid are the owning user and group. The metadata
	// quick check compares them because a chown-only copy-up must
	// not be pruned: deleting it would silently revert the
	// ownership change.
	uid uint32
	gid uint32
}

// PruneReport counts the outcome of one PruneUpper run. Every upper
// entry the walk visits bumps exactly one counter: a removed entry
// bumps one of the Removed fields, a directory that lost its opaque
// marker bumps StrippedOpaque, a kept metacopy stub bumps
// SkippedMetacopy, the root of a kept redirect subtree bumps
// SkippedRedirect, an entry kept after a stat, read, xattr, or
// removal error bumps Errors, and every other kept entry bumps Kept.
// Entries below a skipped subtree are never visited and never
// counted, and an entry that vanishes mid-walk is not counted.
type PruneReport struct {
	// RemovedFiles counts regular files and redundant metacopy
	// stubs deleted.
	RemovedFiles int
	// RemovedLinks counts symlinks deleted.
	RemovedLinks int
	// RemovedWhiteouts counts stale whiteout markers deleted.
	RemovedWhiteouts int
	// RemovedDirs counts directories deleted.
	RemovedDirs int
	// StrippedOpaque counts opaque markers removed from
	// directories that kept their content.
	StrippedOpaque int
	// SkippedMetacopy counts metacopy stubs kept because their
	// metadata no longer matches the lower file.
	SkippedMetacopy int
	// SkippedRedirect counts redirect directories whose whole
	// subtree was kept.
	SkippedRedirect int
	// Kept counts every other entry left alone.
	Kept int
	// Errors counts entries kept fail-safe after an error.
	Errors int
}

// PruneUpper removes the entries under upperDir that lowerView
// already serves identically, so remounting over a changed lower
// stack does not reapply stale upper content. lowerView is the
// read-only merged view of the new lower stack, and upperDir is the
// writable upper of the old mount. Both roots must be real
// directories — symlinks are rejected — and they must be distinct
// and unnested, so the walk can never compare a tree with itself.
// The upperDir itself is never removed; only its descendants are
// pruned, and the root gets the same redirect and opaque marker
// handling every subdirectory gets. The walk is post-order:
// children are cleaned before their parent, so the empty-directory
// rules judge the cleaned state. The function only compares and
// edits directories; it never mounts or unmounts anything.
//
// The comparison is a metadata quick check by design: two regular
// files match when their sizes, full modes (permission bits plus
// the setuid, setgid, and sticky bits), owners, and mtimes
// truncated to whole seconds all agree, because the image format
// stores second granularity. A file rewritten within the same
// second as its baked copy with an unchanged size and metadata can
// therefore be deleted by mistake. This residual risk is accepted
// in exchange for never hashing content. An emptied plain directory
// matches when its mode, owner, and mtime truncated to whole
// seconds agree with the lower directory. Those directory fields
// are taken before children are removed, because a removal
// refreshes the directory mtime.
//
// Every doubtful case is kept fail-safe: a path the lower view does
// not have (including an empty directory), type mismatches, hard
// links, fifos, sockets, device nodes, redirect subtrees, opaque
// subtrees the lower side still fills, and any stat, read, xattr,
// or removal error. Per-entry problems are counted in the report
// and never abort the walk. The returned error reports unusable
// roots, and an empty or unlistable lower view: that view must not
// delete whiteouts.
func PruneUpper(upperDir, lowerView string) (PruneReport, error) {
	return pruneUpper(upperDir, lowerView, pruneHooks{})
}

// pruneUpper runs the walk with the given platform seams and no
// progress output.
func pruneUpper(upperDir, lowerView string, seam pruneHooks) (PruneReport, error) {
	return pruneWithProgress(upperDir, lowerView, seam, nil)
}

// pruneWithProgress runs the walk and, when progress is set, counts
// decided entries against the pre-walk upper total. A nil progress
// value does not count or write.
func pruneWithProgress(
	upperDir, lowerView string,
	seam pruneHooks,
	progress *pruneProgress,
) (PruneReport, error) {
	p := pruner{seam: seam, progress: progress}
	if err := checkPruneRoots(upperDir, lowerView); err != nil {
		return p.report, err
	}
	if err := requireLowerView(lowerView); err != nil {
		return p.report, err
	}
	if p.progress != nil {
		p.progress.setTotal(countTreeEntries(upperDir))
	}
	if err := p.pruneRoot(upperDir, lowerView); err != nil {
		return p.report, err
	}
	return p.report, nil
}

// checkPruneRoots refuses to run unless both sides are distinct
// real directories that contain neither each other nor themselves.
// A missing lowerView must never read as "the lower stack has
// nothing": that would delete every whiteout. An empty but present
// lower view is refused by requireLowerView for the same reason.
// A symlinked root is rejected because the walk would edit the link
// target instead of the directory the caller named, and aliased or
// nested roots are rejected because a tree compared with itself —
// or with a view inside it — matches nearly everything and would be
// deleted wholesale.
func checkPruneRoots(upperDir, lowerView string) error {
	upperInfo, err := lstatPruneRoot(upperDir)
	if err != nil {
		return err
	}
	lowerInfo, err := lstatPruneRoot(lowerView)
	if err != nil {
		return err
	}
	if os.SameFile(upperInfo, lowerInfo) {
		return fmt.Errorf("workspace: upper prune: %s and %s are the same directory", upperDir, lowerView)
	}
	upper, err := resolvedRoot(upperDir)
	if err != nil {
		return err
	}
	lower, err := resolvedRoot(lowerView)
	if err != nil {
		return err
	}
	if upper == lower {
		return fmt.Errorf("workspace: upper prune: %s and %s resolve to the same directory", upperDir, lowerView)
	}
	sep := string(filepath.Separator)
	if strings.HasPrefix(lower, upper+sep) || strings.HasPrefix(upper, lower+sep) {
		return fmt.Errorf("workspace: upper prune: %s and %s nest", upperDir, lowerView)
	}
	return nil
}

// lstatPruneRoot verifies one root is a real directory. The Lstat
// rejects symlinked roots so the walk can never be redirected into
// a tree the caller did not name.
func lstatPruneRoot(dir string) (os.FileInfo, error) {
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, fmt.Errorf("workspace: upper prune: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("workspace: upper prune: %s is not a directory", dir)
	}
	return info, nil
}

// resolvedRoot canonicalizes an accepted root so containment cannot
// hide behind relative path elements or symlinked parents.
func resolvedRoot(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("workspace: upper prune: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("workspace: upper prune: %w", err)
	}
	return resolved, nil
}

// requireLowerView refuses a comparison root that cannot be listed
// or has no entries. Both look like "the lower stack has nothing",
// and walking either one would delete every whiteout whose path is
// absent. The caller has not modified the upper tree yet.
func requireLowerView(lowerView string) error {
	entries, err := os.ReadDir(lowerView)
	if err != nil {
		return fmt.Errorf("workspace: upper prune: cannot list %s: %w", lowerView, err)
	}
	if len(entries) == 0 {
		return fmt.Errorf("workspace: upper prune: %s has no entries", lowerView)
	}
	return nil
}

// pruneHooks are test seams for the platform probes the prune rules
// need. The zero value probes the real platform state through the
// Linux-only helpers; off Linux every probe fails and the rules
// keep all entries.
type pruneHooks struct {
	// xattrs returns the trusted.overlay.* xattrs of a path.
	xattrs func(path string) (map[string][]byte, error)
	// strip removes one xattr from a path.
	strip func(path, name string) error
	// stat returns the raw stat fields of an entry the walk
	// already Lstat'd; info is that Lstat's result.
	stat func(path string, info os.FileInfo) (platformStat, error)
}

func (h pruneHooks) xattrsOf(path string) (map[string][]byte, error) {
	if h.xattrs != nil {
		return h.xattrs(path)
	}
	return overlayXattrs(path)
}

func (h pruneHooks) stripXattr(path, name string) error {
	if h.strip != nil {
		return h.strip(path, name)
	}
	return stripOverlayXattr(path, name)
}

// statOf returns the raw stat fields of an already-Lstat'd entry.
// The real probe derives them from the walk's own FileInfo, so one
// syscall backs every field of one rule decision.
func (h pruneHooks) statOf(path string, info os.FileInfo) (platformStat, error) {
	if h.stat != nil {
		return h.stat(path, info)
	}
	return platformStatOf(info)
}

// pruner walks one upper tree against its lower comparison view and
// collects the report.
type pruner struct {
	seam     pruneHooks
	report   PruneReport
	progress *pruneProgress
}

// note advances the progress numerator by n decided entries. A nil
// progress writer stays silent, and n is zero when nothing new was
// decided.
func (p *pruner) note(n int) {
	if p.progress == nil {
		return
	}
	p.progress.advance(n)
}

// skipRedirect keeps a redirect directory and, in one step, counts
// the directory itself (when it is an upper entry) plus every
// unvisited descendant. The report still counts only the directory.
func (p *pruner) skipRedirect(dir string, includeSelf bool) {
	p.report.SkippedRedirect++
	if p.progress == nil {
		return
	}
	n := countTreeEntries(dir)
	if includeSelf {
		n++
	}
	p.note(n)
}

// keepFuseOpaque preserves the marker and children together. Removing a
// baked child while the FUSE opacity marker survives would hide its lower copy.
func (p *pruner) keepFuseOpaque(dir string, includeSelf bool) {
	p.report.Kept++
	if p.progress != nil {
		n := countTreeEntries(dir)
		if includeSelf {
			n++
		}
		p.note(n)
	}
}

// countTreeEntries counts descendants of dir, not dir itself. The
// walk stats names only; it does not read regular-file contents.
func countTreeEntries(dir string) int {
	n := 0
	_ = filepath.WalkDir(dir, func(path string, _ os.DirEntry, err error) error {
		if err != nil || path == dir {
			return nil
		}
		n++
		return nil
	})
	return n
}

// pruneRoot gives the upper root the same marker handling every
// subdirectory gets in pruneDir, minus the removals: a redirect
// shifts the comparison baseline of the whole tree, and an opaque
// marker over lower content forbids pruning the children the marker
// hides. The root itself is never removed. An unreadable marker set
// keeps the whole tree fail-safe. The returned error reports an
// unusable root listing.
func (p *pruner) pruneRoot(upperDir, lowerView string) error {
	xattrs, err := p.seam.xattrsOf(upperDir)
	if err != nil {
		p.report.Errors++
		return nil
	}
	if _, ok := xattrs[xattrFuseOpaque]; ok {
		p.keepFuseOpaque(upperDir, false)
		return nil
	}
	if _, ok := xattrs[xattrRedirect]; ok {
		p.skipRedirect(upperDir, false)
		return nil
	}
	if _, ok := xattrs[xattrOpaque]; ok {
		return p.pruneRootOpaque(upperDir, lowerView)
	}
	return p.cleanRootChildren(upperDir, lowerView)
}

// pruneRootOpaque cleans an opaque upper root the lower side has
// nothing to fill and then drops the stale marker. The root is
// never removed, so stripping is the root's variant of the
// empty-opaque-directory rule.
func (p *pruner) pruneRootOpaque(upperDir, lowerView string) error {
	state, err := probeLowerDir(lowerView)
	if err != nil {
		p.report.Errors++
		return nil
	}
	if state == lowerHasContent {
		p.report.Kept++
		return nil
	}
	if err := p.cleanRootChildren(upperDir, lowerView); err != nil {
		return err
	}
	if err := p.seam.stripXattr(upperDir, xattrOpaque); err != nil {
		p.report.Errors++
		return nil
	}
	p.report.StrippedOpaque++
	return nil
}

// cleanRootChildren cleans the root's children and escalates an
// unusable root listing to the caller-visible error.
func (p *pruner) cleanRootChildren(upperDir, lowerView string) error {
	if _, listed := p.cleanChildren(upperDir, lowerView); !listed {
		return fmt.Errorf("workspace: upper prune: cannot list %s", upperDir)
	}
	return nil
}

// cleanChildren prunes every entry below dir and reports whether dir
// is empty afterwards and whether its listing worked at all. The
// post-clean listing is what the empty-directory rules judge, so
// children are always cleaned first.
func (p *pruner) cleanChildren(dir, lowerDir string) (empty, listed bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		p.report.Errors++
		return false, false
	}
	for _, entry := range entries {
		name := entry.Name()
		p.pruneEntry(filepath.Join(dir, name), filepath.Join(lowerDir, name))
	}
	after, err := os.ReadDir(dir)
	if err != nil {
		p.report.Errors++
		return false, false
	}
	return len(after) == 0, true
}

// pruneEntry applies the rule table to one upper entry. An entry
// that vanishes between listing and stat is silently ignored; every
// other error keeps the entry fail-safe.
func (p *pruner) pruneEntry(path, lowerPath string) {
	info, err := os.Lstat(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			p.report.Errors++
			p.note(1)
		}
		return
	}
	if info.IsDir() {
		p.pruneDir(path, lowerPath, info)
		return
	}
	defer p.note(1)
	st, err := p.seam.statOf(path, info)
	if err != nil {
		p.report.Errors++
		return
	}
	switch {
	case st.nlink > 1:
		// Hard-linked entries are kept fail-safe.
		p.report.Kept++
	case info.Mode()&os.ModeSymlink != 0:
		p.pruneSymlink(path, lowerPath)
	case info.Mode()&os.ModeCharDevice != 0:
		p.pruneChardev(path, lowerPath, st.rdev)
	case info.Mode().IsRegular():
		p.pruneFile(path, lowerPath, info, st)
	default:
		// Fifos, sockets, and block devices are kept fail-safe.
		p.report.Kept++
	}
}

// pruneDir resolves the directory overlay markers before touching
// any child: a redirect shifts the comparison baseline of the whole
// subtree, and an opaque marker changes what children may be
// compared against. An unreadable marker set keeps the whole
// subtree fail-safe.
func (p *pruner) pruneDir(path, lowerPath string, info os.FileInfo) {
	xattrs, err := p.seam.xattrsOf(path)
	if err != nil {
		p.report.Errors++
		p.note(1)
		return
	}
	if _, ok := xattrs[xattrFuseOpaque]; ok {
		p.keepFuseOpaque(path, true)
		return
	}
	if _, ok := xattrs[xattrRedirect]; ok {
		p.skipRedirect(path, true)
		return
	}
	defer p.note(1)
	if _, ok := xattrs[xattrOpaque]; ok {
		p.pruneOpaqueDir(path, lowerPath)
		return
	}
	empty, listed := p.cleanChildren(path, lowerPath)
	if !listed {
		return
	}
	p.prunePlainDir(path, lowerPath, info, empty)
}

// pruneOpaqueDir handles a directory marked opaque. The marker hides
// every lower entry below the directory, so the subtree is only
// cleaned when the lower side has nothing to hide: with lower
// content, removing children that match the lower view would erase
// them from the merged result. A cleaned directory that is now
// empty goes away with its marker; a non-empty one keeps its
// children but loses the stale marker.
func (p *pruner) pruneOpaqueDir(path, lowerPath string) {
	state, err := probeLowerDir(lowerPath)
	if err != nil {
		p.report.Errors++
		return
	}
	if state == lowerHasContent {
		p.report.Kept++
		return
	}
	empty, listed := p.cleanChildren(path, lowerPath)
	if !listed {
		return
	}
	if empty {
		p.remove(path, &p.report.RemovedDirs)
		return
	}
	if err := p.seam.stripXattr(path, xattrOpaque); err != nil {
		p.report.Errors++
		return
	}
	p.report.StrippedOpaque++
}

// lowerDirState classifies the lower counterpart of an upper
// directory.
type lowerDirState int

const (
	// lowerAbsent means the lower side has no entry at the path.
	lowerAbsent lowerDirState = iota
	// lowerEmpty means an empty lower directory.
	lowerEmpty
	// lowerHasContent means a non-empty lower directory, or a
	// non-directory entry conflicting with the upper directory.
	lowerHasContent
)

// probeLowerDir classifies one lower path. A conflicting type counts
// as content so type mismatches always resolve conservatively.
func probeLowerDir(path string) (lowerDirState, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return lowerAbsent, nil
	}
	if err != nil {
		return lowerHasContent, err
	}
	if !info.IsDir() {
		return lowerHasContent, nil
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return lowerHasContent, err
	}
	if len(entries) == 0 {
		return lowerEmpty, nil
	}
	return lowerHasContent, nil
}

// prunePlainDir removes an emptied plain directory only when the
// lower side is a directory with the same mode, owner, and
// second-granularity mtime. info is the upper directory as it was
// before children were cleaned, so the mtime comparison is not
// poisoned by those removals. A path the lower view does not have
// stays, including an empty directory. A type mismatch or a
// metadata difference stays too.
func (p *pruner) prunePlainDir(path, lowerPath string, info os.FileInfo, empty bool) {
	if !empty {
		p.report.Kept++
		return
	}
	lowerInfo, err := os.Lstat(lowerPath)
	if errors.Is(err, os.ErrNotExist) {
		p.report.Kept++
		return
	}
	if err != nil {
		p.report.Errors++
		return
	}
	if !lowerInfo.IsDir() {
		p.report.Kept++
		return
	}
	st, err := p.seam.statOf(path, info)
	if err != nil {
		p.report.Errors++
		return
	}
	lowerSt, err := p.seam.statOf(lowerPath, lowerInfo)
	if err != nil {
		p.report.Errors++
		return
	}
	if !sameDirMeta(info, lowerInfo, st, lowerSt) {
		p.report.Kept++
		return
	}
	p.remove(path, &p.report.RemovedDirs)
}

// sameDirMeta is the metadata quick check for two directories:
// full mode, ownership, and mtime truncated to whole seconds.
// Directory size is not compared. Both sides are already verified
// directories, so the full mode adds the setgid and sticky bits.
func sameDirMeta(upper, lower os.FileInfo, upperSt, lowerSt platformStat) bool {
	return upper.Mode() == lower.Mode() &&
		upper.ModTime().Unix() == lower.ModTime().Unix() &&
		upperSt.uid == lowerSt.uid &&
		upperSt.gid == lowerSt.gid
}

// pruneFile judges one regular file. A file the lower stack does
// not have is new user data and stays; otherwise the metadata quick
// check decides. The st fields describe the same Lstat as info.
func (p *pruner) pruneFile(path, lowerPath string, info os.FileInfo, st platformStat) {
	xattrs, err := p.seam.xattrsOf(path)
	if err != nil {
		p.report.Errors++
		return
	}
	if _, ok := xattrs[xattrMetacopy]; ok {
		p.pruneMetacopy(path, lowerPath, info, st)
		return
	}
	lowerInfo, err := os.Lstat(lowerPath)
	if errors.Is(err, os.ErrNotExist) {
		p.report.Kept++
		return
	}
	if err != nil {
		p.report.Errors++
		return
	}
	if !lowerInfo.Mode().IsRegular() {
		// Type mismatch: keep.
		p.report.Kept++
		return
	}
	lowerSt, err := p.seam.statOf(lowerPath, lowerInfo)
	if err != nil {
		p.report.Errors++
		return
	}
	if !sameFileMeta(info, lowerInfo, st, lowerSt) {
		// A metadata difference: keep.
		p.report.Kept++
		return
	}
	p.remove(path, &p.report.RemovedFiles)
}

// pruneMetacopy judges a metacopy stub: its data already lives in
// the lower stack, so the stub is redundant only when the metadata
// the merged view would present — the upper mode, owner, and mtime
// with the lower size — fully matches the lower file. A stub that
// carries data itself is corrupt and stays. Anything else keeps the
// stub.
func (p *pruner) pruneMetacopy(path, lowerPath string, info os.FileInfo, st platformStat) {
	lowerInfo, err := os.Lstat(lowerPath)
	if errors.Is(err, os.ErrNotExist) {
		p.report.SkippedMetacopy++
		return
	}
	if err != nil {
		p.report.Errors++
		return
	}
	if !lowerInfo.Mode().IsRegular() {
		p.report.SkippedMetacopy++
		return
	}
	lowerSt, err := p.seam.statOf(lowerPath, lowerInfo)
	if err != nil {
		p.report.Errors++
		return
	}
	if info.Size() != 0 ||
		info.Mode() != lowerInfo.Mode() ||
		st.uid != lowerSt.uid || st.gid != lowerSt.gid ||
		info.ModTime().Unix() != lowerInfo.ModTime().Unix() {
		p.report.SkippedMetacopy++
		return
	}
	p.remove(path, &p.report.RemovedFiles)
}

// pruneSymlink removes a symlink whose target matches the lower
// view. A lower path that is missing or is not a symlink is a type
// mismatch and keeps the link.
func (p *pruner) pruneSymlink(path, lowerPath string) {
	target, err := os.Readlink(path)
	if err != nil {
		p.report.Errors++
		return
	}
	lowerInfo, err := os.Lstat(lowerPath)
	if errors.Is(err, os.ErrNotExist) {
		p.report.Kept++
		return
	}
	if err != nil {
		p.report.Errors++
		return
	}
	if lowerInfo.Mode()&os.ModeSymlink == 0 {
		// Type mismatch: the lower side is not a symlink.
		p.report.Kept++
		return
	}
	lowerTarget, err := os.Readlink(lowerPath)
	if err != nil {
		p.report.Errors++
		return
	}
	if target != lowerTarget {
		p.report.Kept++
		return
	}
	p.remove(path, &p.report.RemovedLinks)
}

// pruneChardev judges a character device node. A node with a real
// device number is user data and stays. A zero-rdev node is an
// overlay whiteout: it is stale only when the lower view no longer
// has the path it hides, meaning the deletion is baked into the new
// stack. While the lower still serves the path, the whiteout is
// what hides it and must stay.
func (p *pruner) pruneChardev(path, lowerPath string, rdev uint64) {
	if rdev != 0 {
		p.report.Kept++
		return
	}
	_, err := os.Lstat(lowerPath)
	switch {
	case err == nil:
		p.report.Kept++
	case errors.Is(err, os.ErrNotExist):
		p.remove(path, &p.report.RemovedWhiteouts)
	default:
		p.report.Errors++
	}
}

// remove deletes one entry and counts it, or counts the failure. A
// failed removal keeps the entry, which is always safe; os.Remove
// refuses non-empty directories, so a concurrent refill keeps a
// directory too. An entry that vanished between the prune decision
// and the removal already reached its intended end state, and
// vanished entries are not counted at all.
func (p *pruner) remove(path string, counter *int) {
	err := os.Remove(path)
	switch {
	case err == nil:
		*counter++
	case errors.Is(err, os.ErrNotExist):
		// Gone mid-walk: a benign race, not an error.
	default:
		p.report.Errors++
	}
}

// sameFileMeta is the metadata quick check for two regular files:
// size, full mode, ownership, and mtime truncated to whole seconds
// because the image format stores second granularity. Comparing the
// full mode instead of the permission bits, and the owners as well,
// keeps chmod-only and chown-only copy-ups: deleting such a copy
// would silently revert the mode or ownership change, losing a
// setuid bit being a security regression. Both sides are already
// verified regular files, so the full mode only adds the setuid,
// setgid, and sticky bits to the comparison.
func sameFileMeta(upper, lower os.FileInfo, upperSt, lowerSt platformStat) bool {
	return upper.Size() == lower.Size() &&
		upper.Mode() == lower.Mode() &&
		upper.ModTime().Unix() == lower.ModTime().Unix() &&
		upperSt.uid == lowerSt.uid &&
		upperSt.gid == lowerSt.gid
}
