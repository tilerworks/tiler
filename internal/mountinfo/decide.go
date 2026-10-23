package mountinfo

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

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

// Name is an image identity: one canonical 1-to-5 segment library
// name.
type Name string

func (n Name) String() string { return string(n) }

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
	// Image is the --image library name, empty when omitted.
	Image string
	// Stale marks a path the caller declares was a mount point that
	// the table no longer lists.
	Stale bool
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
	// is usually the mount point. When --image names a different
	// library image, SrcDir is the cleaned path (often a subdirectory
	// of the mount) so that path packs as its own full image.
	SrcDir string
	// MountPoint is the view mount point the path resolved to.
	// Empty when the build packs a plain directory, including a
	// diverted subdirectory under a view.
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
		return decideView(cwd, view, table, prefixes, explicit)
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

func decideView(cwd string, view Mount, table []Mount, prefixes Prefixes, explicit Explicit) (Decision, error) {
	// --image naming a different library image packs the cleaned
	// path as that image's full source. It is not an increment of
	// the live view and does not bake a dmg shadow.
	diverted, ok, err := divertDifferentImage(cwd, view, table, prefixes, explicit)
	if err != nil {
		return Decision{}, err
	}
	if ok {
		return diverted, nil
	}
	if view.Upper == "" {
		return decideReadOnlyView(view, table, prefixes, explicit)
	}
	// A dmg shadow has no incremental slot. The build is a new full
	// image at the mount point, and --incremental selects that full
	// image. Omitting dir, ".", and the absolute path share this
	// decision: only the cleaned path is consulted.
	if explicit.Full || dmgShadowView(view) {
		return fullInView(view, table, prefixes, explicit)
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

// divertDifferentImage reports a full build of cwd into an explicit
// --image that is not the mounted view. --incremental with a
// mismatched name stays a conflict.
func divertDifferentImage(
	cwd string, view Mount, table []Mount, prefixes Prefixes, explicit Explicit,
) (Decision, bool, error) {
	if explicit.Image == "" {
		return Decision{}, false, nil
	}
	segs, err := image.ParseName(explicit.Image)
	if err != nil {
		return Decision{}, false, err
	}
	given := Name(strings.Join(segs, "/"))
	looked, found, err := lookupViewName(view.Point, table, prefixes, explicit)
	if err != nil {
		return Decision{}, false, err
	}
	if !found || given == looked {
		return Decision{}, false, nil
	}
	if explicit.Incr {
		return Decision{}, false, fmt.Errorf(
			"build identity: --image %s conflicts with the mounted image %s", given, looked)
	}
	return Decision{Mode: ModeFull, Name: given, SrcDir: cwd}, true, nil
}

func lookupViewName(
	viewPoint string, table []Mount, prefixes Prefixes, explicit Explicit,
) (Name, bool, error) {
	identity := explicit.Identity
	if identity == nil {
		identity = PathIdentity{Table: table, ImagesDir: prefixes.ImagesDir}
	}
	return identity.Lookup(viewPoint)
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

// dmgShadowView reports a writable disk image whose upper is the
// live shadow file. That form publishes a new full image.
func dmgShadowView(view Mount) bool {
	return filepath.Base(view.Upper) == "upper.shadow"
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
			return "", err
		}
		looked, found = name, ok
	}
	if explicit.Image == "" {
		if !found {
			return "", errors.New(
				"build identity: cannot infer image name; pass an explicit --image")
		}
		return looked, nil
	}
	segs, err := image.ParseName(explicit.Image)
	if err != nil {
		return "", err
	}
	given := Name(strings.Join(segs, "/"))
	if found && given != looked {
		return "", fmt.Errorf(
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
		return "", false, nil
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
			return "", false, fmt.Errorf(
				"build identity: mount %s mixes images %s and %s", mountPoint, found, name)
		}
	}
	return found, seen, nil
}

// LibraryName maps a library file path to its image identity. Only
// <images_dir>/<name>/image.sqsh, <images_dir>/<name>/image.dmg, and
// <images_dir>/<name>/_incr/<id>.sqsh count, where <name> is 1 to 5
// segments. Paths under _archives, _hold, or _blocks never carry an
// identity. Blocks-form mounts publish the canonical image.sqsh
// path, so they resolve without a blocks shape here.
func LibraryName(imagesDir, path string) (Name, bool) {
	if imagesDir == "" || !strings.HasPrefix(path, imagesDir+"/") {
		return "", false
	}
	parts := strings.Split(path[len(imagesDir)+1:], "/")
	for _, p := range parts {
		switch p {
		case "_archives", "_hold", "_blocks":
			return "", false
		}
	}
	var nameParts []string
	switch {
	case len(parts) >= 2 &&
		(parts[len(parts)-1] == image.ImageFile || parts[len(parts)-1] == image.ImageDMG):
		nameParts = parts[:len(parts)-1]
	case len(parts) >= 3 &&
		parts[len(parts)-2] == image.IncrDir && isLayerFile(parts[len(parts)-1]):
		nameParts = parts[:len(parts)-2]
	default:
		return "", false
	}
	segs, err := image.ParseName(strings.Join(nameParts, "/"))
	if err != nil {
		return "", false
	}
	return Name(strings.Join(segs, "/")), true
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
