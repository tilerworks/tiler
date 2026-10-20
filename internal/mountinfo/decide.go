package mountinfo

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/jiangxin/tiler/internal/image"
)

// Mode is the resolved build kind.
type Mode string

const (
	// ModeFull packs a whole directory into a new full image.
	ModeFull Mode = "full"
	// ModeIncr packs the upper of a merged view as one more layer.
	ModeIncr Mode = "incremental"
)

// Name is an image identity: an explicit owner/repo pair.
type Name struct {
	Owner string
	Repo  string
}

func (n Name) String() string { return n.Owner + "/" + n.Repo }

// Prefixes locates the reserved trees for one decision.
type Prefixes struct {
	// RuntimeDir is the prefix of per-mount runtime directories.
	RuntimeDir string
	// ImagesDir is the root of the image library.
	ImagesDir string
}

// Explicit carries the caller flags for one decision.
type Explicit struct {
	// Full is the --full flag.
	Full bool
	// Incr is the --incremental flag.
	Incr bool
	// Image is the --image owner/repo value, empty when omitted.
	Image string
	// Stale marks a path the caller declares was a mount point that
	// the table no longer lists.
	Stale bool
	// HasDir marks an explicit source directory argument.
	HasDir bool
	// Identity replaces the default path lookup. Nil keeps it.
	Identity IdentitySource
}

// Decision is the resolved build plan.
type Decision struct {
	// Mode is full or incremental.
	Mode Mode
	// Name is the resolved image identity.
	Name Name
	// SrcDir is the directory a full build packs. Inside a view it
	// is the mount point, never a deeper path.
	SrcDir string
	// MountPoint is the view mount point the path resolved to.
	MountPoint string
	// Upper is the writable upper directory of an incremental build.
	Upper string
}

// IdentitySource maps a mount point to an image identity. Session
// management provides a richer implementation later; the default
// only inspects mount source paths and never reads session records.
type IdentitySource interface {
	Lookup(mountPoint string) (Name, bool, error)
}

// Decide resolves the build mode and identity for one cleaned path
// against an injected mount table. It matches the table before
// touching any path and never stats, so a dead mount can still be
// named. A path under the runtime prefix, a stale mount marker, a
// read-only view without --full, and conflicting identity sources
// are all refused.
func Decide(cwd string, table []Mount, prefixes Prefixes, explicit Explicit) (Decision, error) {
	if explicit.Full && explicit.Incr {
		return Decision{}, errors.New("build mode: --full and --incremental are mutually exclusive")
	}
	cwd = filepath.Clean(cwd)
	if underPrefix(cwd, prefixes.RuntimeDir) {
		return Decision{}, fmt.Errorf(
			"build mode: %s is inside the runtime directory, not a workspace", cwd)
	}
	if explicit.Stale {
		return Decision{}, fmt.Errorf(
			"build mode: %s is a stale mount point missing from the mount table", cwd)
	}
	if view, ok := LongestView(table, cwd); ok {
		return decideView(view, table, prefixes, explicit)
	}
	return decidePlain(cwd, prefixes, explicit)
}

func decidePlain(cwd string, prefixes Prefixes, explicit Explicit) (Decision, error) {
	if explicit.Incr {
		return Decision{}, fmt.Errorf(
			"build mode: --incremental needs a merged view; %s is a plain directory", cwd)
	}
	name, err := resolveName("", nil, prefixes, explicit)
	if err != nil {
		return Decision{}, err
	}
	return Decision{Mode: ModeFull, Name: name, SrcDir: cwd}, nil
}

func decideView(view Mount, table []Mount, prefixes Prefixes, explicit Explicit) (Decision, error) {
	if view.Upper == "" {
		return decideReadOnlyView(view, table, prefixes, explicit)
	}
	if explicit.Full {
		return fullInView(view, table, prefixes, explicit)
	}
	if explicit.HasDir {
		return Decision{}, errors.New("build mode: incremental mode takes no source directory")
	}
	name, err := resolveName(view.Point, table, prefixes, explicit)
	if err != nil {
		return Decision{}, err
	}
	return Decision{
		Mode:       ModeIncr,
		Name:       name,
		MountPoint: view.Point,
		Upper:      view.Upper,
	}, nil
}

func decideReadOnlyView(view Mount, table []Mount, prefixes Prefixes, explicit Explicit) (Decision, error) {
	if explicit.Incr {
		return Decision{}, fmt.Errorf(
			"build mode: %s is a read-only view without an upper; incremental builds need a writable view",
			view.Point)
	}
	if !explicit.Full {
		return Decision{}, fmt.Errorf(
			"build mode: %s is a read-only view; pass --full to pack it as a new full image",
			view.Point)
	}
	return fullInView(view, table, prefixes, explicit)
}

// fullInView packs the whole merged view. The deeper path is
// ignored; the mount point is the source directory.
func fullInView(view Mount, table []Mount, prefixes Prefixes, explicit Explicit) (Decision, error) {
	name, err := resolveName(view.Point, table, prefixes, explicit)
	if err != nil {
		return Decision{}, err
	}
	return Decision{
		Mode:       ModeFull,
		Name:       name,
		SrcDir:     view.Point,
		MountPoint: view.Point,
	}, nil
}

// resolveName settles the image identity. A successful lookup must
// be unique and equal an explicit --image; without either side the
// identity cannot be settled.
func resolveName(viewPoint string, table []Mount, prefixes Prefixes, explicit Explicit) (Name, error) {
	var (
		looked Name
		found  bool
	)
	if viewPoint != "" {
		identity := explicit.Identity
		if identity == nil {
			identity = PathIdentity{Table: table, ImagesDir: prefixes.ImagesDir}
		}
		name, ok, err := identity.Lookup(viewPoint)
		if err != nil {
			return Name{}, err
		}
		looked, found = name, ok
	}
	if explicit.Image == "" {
		if !found {
			return Name{}, errors.New(
				"build identity: cannot infer owner/repo; pass an explicit --image")
		}
		return looked, nil
	}
	owner, repo, err := image.ParseName(explicit.Image)
	if err != nil {
		return Name{}, err
	}
	given := Name{Owner: owner, Repo: repo}
	if found && given != looked {
		return Name{}, fmt.Errorf(
			"build identity: --image %s conflicts with the mounted image %s", given, looked)
	}
	return given, nil
}

// LongestView is the view mount whose point is path or a directory
// prefix of it, choosing the longest such point. The build decision
// and the session reverse lookup share it, so the deepest covering
// view always wins the same way.
func LongestView(table []Mount, path string) (Mount, bool) {
	best := Mount{}
	found := false
	for _, mount := range table {
		if !isViewMount(mount) || !underPrefix(path, mount.Point) {
			continue
		}
		if !found || len(mount.Point) > len(best.Point) {
			best, found = mount, true
		}
	}
	return best, found
}

func underPrefix(path, prefix string) bool {
	return prefix != "" && (path == prefix || strings.HasPrefix(path, prefix+"/"))
}

// PathIdentity resolves identities from mount source paths inside
// the image library. A merged view resolves each lower through the
// table, and every resolvable lower must belong to the same image.
type PathIdentity struct {
	Table     []Mount
	ImagesDir string
}

// Lookup returns the identity behind a mount point when its sources
// point into the image library.
func (p PathIdentity) Lookup(mountPoint string) (Name, bool, error) {
	view, ok := findMount(p.Table, mountPoint)
	if !ok {
		return Name{}, false, nil
	}
	candidates := append([]string{view.Source}, view.Lowers...)
	var (
		found Name
		seen  bool
	)
	for _, candidate := range candidates {
		source := candidate
		if mount, ok := findMount(p.Table, candidate); ok && mount.Source != "" {
			source = mount.Source
		}
		name, ok := LibraryName(p.ImagesDir, source)
		if !ok {
			continue
		}
		switch {
		case !seen:
			found, seen = name, true
		case name != found:
			return Name{}, false, fmt.Errorf(
				"build identity: mount %s mixes images %s and %s", mountPoint, found, name)
		}
	}
	return found, seen, nil
}

// LibraryName maps a library file path to its image identity. Only
// <images_dir>/<owner>/<repo>/image.sqsh,
// <images_dir>/<owner>/<repo>/incr/<id>.sqsh, and the members-form
// partition file <images_dir>/<owner>/<repo>/members/<name>.sqsh
// count; archives, holds, blocks, and metadata never carry an
// identity. Blocks-form mounts publish the canonical three-segment
// image.sqsh path, so they resolve without a blocks shape here.
func LibraryName(imagesDir, path string) (Name, bool) {
	if imagesDir == "" || !strings.HasPrefix(path, imagesDir+"/") {
		return Name{}, false
	}
	parts := strings.Split(path[len(imagesDir)+1:], "/")
	switch len(parts) {
	case 3:
		if parts[2] != image.ImageFile {
			return Name{}, false
		}
	case 4:
		switch {
		case parts[2] == image.IncrDir && isLayerFile(parts[3]):
		case parts[2] == image.MembersDir && isMemberFile(parts[3]):
		default:
			return Name{}, false
		}
	default:
		return Name{}, false
	}
	owner, repo, err := image.ParseName(parts[0] + "/" + parts[1])
	if err != nil {
		return Name{}, false
	}
	return Name{Owner: owner, Repo: repo}, true
}

// isMemberFile reports a members-form <name>.sqsh file name whose
// stem is one valid path segment, mirroring the member name rules
// the image library validates at build time.
func isMemberFile(name string) bool {
	stem, ok := strings.CutSuffix(name, image.SquashExt)
	if !ok || stem == "" || stem == "." || stem == ".." {
		return false
	}
	for _, r := range stem {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

// isLayerFile reports a <id>.sqsh name with a 16 lowercase hex id.
func isLayerFile(name string) bool {
	id, ok := strings.CutSuffix(name, image.SquashExt)
	if !ok || len(id) != 16 {
		return false
	}
	for _, r := range id {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
