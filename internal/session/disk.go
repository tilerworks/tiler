package session

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/jiangxin/tiler/internal/image"
)

// diskShadowFile is the writable UDIF shadow name. It matches the
// container package's upper.shadow layout:
// sessions/<name>/runtime/m<seq>/upper.shadow.
const diskShadowFile = "upper.shadow"

// diskImageFile is the active dmg payload inside a library
// repository. With the cache off, hdiutil attach opens this file.
// The session record stores the library name reference.
const diskImageFile = "image.dmg"

// DiskImage is one mounted volume from hdiutil info -plist.
type DiskImage struct {
	// Point is the volume mount point.
	Point string
	// Image is the attached disk image path.
	Image string
	// Shadow is the UDIF shadow path. A read-only attach leaves it
	// empty.
	Shadow string
}

// useDiskImages reports whether status and open should reconcile
// through hdiutil info. An injected Health seam keeps the mount
// table path the Linux tests drive.
func (e *Engine) useDiskImages() bool {
	return e.LoadDiskImages != nil && e.Health == nil
}

// diskListed reports whether hdiutil info still has point attached.
// A point the listing omits is down. Mount points are compared
// after symlink resolution, the same way info and the session
// record spell a /tmp prefix.
func diskListed(images []DiskImage) HealthFunc {
	return func(point string) bool {
		for _, image := range images {
			if SameMountPoint(image.Point, point) {
				return true
			}
		}
		return false
	}
}

// diskImages loads one hdiutil info snapshot.
func (e *Engine) diskImages() ([]DiskImage, error) {
	images, err := e.LoadDiskImages()
	if err != nil {
		return nil, fmt.Errorf("session: %w", err)
	}
	return images, nil
}

// overviewFromDisk reconciles records with one hdiutil info snapshot.
// A live mount is healthy only when the mount point, the image path,
// and the shadow path all match the record. A record that says
// mounted while info has no such volume is stale. An info volume
// whose image lies under the image library, or whose shadow lies in
// a session runtime, is an orphan when no active record claims its
// mount point.
func (e *Engine) overviewFromDisk(current string) (Overview, error) {
	names, err := e.names()
	if err != nil {
		return Overview{}, err
	}
	images, err := e.diskImages()
	if err != nil {
		return Overview{}, err
	}
	overview := Overview{Current: current}
	claimed := map[string]bool{}
	for _, name := range names {
		// Point health is replaced below. The placeholder keeps the
		// shared record read from calling the Linux mount probe.
		view, err := e.sessionView(name, current, func(string) bool { return false })
		if err != nil {
			return Overview{}, err
		}
		if view == nil {
			continue
		}
		if view.State == StateActive && !view.Locked {
			view.Actions = annotateDisk(e, name, view.Actions, images)
		}
		collectClaimed(claimed, *view)
		overview.Sessions = append(overview.Sessions, *view)
	}
	overview.Orphans = diskOrphans(images, claimedPoints(claimed), e.ImagesDir, e.SessionsDir)
	return overview, nil
}

// annotateDisk sets healthy or stale from the three-way info match.
// A torn-down mount stays unannotated.
func annotateDisk(e *Engine, name string, actions []ActionView, images []DiskImage) []ActionView {
	for i := range actions {
		action := actions[i].Action
		actions[i].Health = ""
		if action.Type != TypeMount || action.Status == StatusUnmounted || action.Mountpoint == "" {
			continue
		}
		actions[i].Health = HealthStale
		if e.diskMatch(name, action, images) {
			actions[i].Health = HealthHealthy
		}
	}
	return actions
}

// diskMatch reports whether info has this action's mount point,
// image path, and shadow path. Mount points are compared after
// symlink resolution. Image and shadow paths stay the spellings
// attach passed: hdiutil does not resolve those.
func (e *Engine) diskMatch(name string, action Action, images []DiskImage) bool {
	if action.Mountpoint == "" {
		return false
	}
	wantImage := e.attachImages(action)
	wantShadow := recordedShadow(name, action, e.SessionsDir)
	for _, have := range images {
		if !SameMountPoint(have.Point, action.Mountpoint) ||
			!samePath(have.Shadow, wantShadow) {
			continue
		}
		for _, want := range wantImage {
			if samePath(have.Image, want) {
				return true
			}
		}
	}
	return false
}

// attachImages lists the files hdiutil attach may have opened for
// this action. A library mount with the cache off uses image.dmg.
// With the cache on, attach opens the cache copy and the library
// file still counts. A file-path image is the absolute path attach
// used, plus the cache copy when the cache is on.
func (e *Engine) attachImages(action Action) []string {
	library := action.Name != "" && e.ImagesDir != ""
	primary := recordedImage(action, e.ImagesDir)
	if !library && primary != "" {
		if abs, err := filepath.Abs(primary); err == nil {
			primary = abs
		}
	}
	paths := make([]string, 0, 2)
	if primary != "" {
		paths = append(paths, primary)
	}
	if cache := e.cachedAttachPath(action); cache != "" && !samePath(cache, primary) {
		paths = append(paths, cache)
	}
	if len(paths) == 0 {
		paths = append(paths, "")
	}
	return paths
}

// cachedAttachPath is <CacheDir>/<sha256>.sqsh, the file attach
// opens when the cache is on. A library digest comes from metadata.
// A file-path digest is the hash of the absolute file. The cache
// off, or a digest that cannot be named, has no cache candidate.
func (e *Engine) cachedAttachPath(action Action) string {
	if !e.CacheEnabled || e.CacheDir == "" {
		return ""
	}
	var (
		sum string
		ok  bool
	)
	if action.Name != "" && e.ImagesDir != "" {
		sum, ok = e.libraryDigest(action)
	} else if action.Image != "" {
		abs, err := filepath.Abs(action.Image)
		if err != nil {
			return ""
		}
		sum, ok = fileDigest(abs)
	}
	if !ok {
		return ""
	}
	return filepath.Join(e.CacheDir, sum+image.SquashExt)
}

// libraryDigest is the sha256 metadata records for the active
// library payload. attach uses that digest as the cache file name.
func (e *Engine) libraryDigest(action Action) (string, bool) {
	repo, err := image.OpenRepo(e.ImagesDir, action.Name)
	if err != nil {
		return "", false
	}
	meta, err := image.Load(repo.MetaPath())
	if err != nil || meta.SHA256 == "" {
		return "", false
	}
	return meta.SHA256, true
}

// fileDigest is the lowercase sha256 of path. attach hashes a
// file-path image the same way when the recorded member digest is
// empty.
func fileDigest(path string) (string, bool) {
	file, err := os.Open(path)
	if err != nil {
		return "", false
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil {
		return "", false
	}
	return hex.EncodeToString(hash.Sum(nil)), true
}

// recordedImage is the library payload, or the file path stored on
// the action. attachImages turns a relative file path into the
// absolute path attach used and adds the cache copy when the cache
// is on. A library mount with the cache off stays on image.dmg.
func recordedImage(action Action, imagesDir string) string {
	if action.Name != "" && imagesDir != "" {
		repo, err := image.OpenRepo(imagesDir, action.Name)
		if err != nil {
			return ""
		}
		return filepath.Join(repo.Dir, diskImageFile)
	}
	return action.Image
}

// recordedShadow is the writable shadow for one mount action, or
// empty for a read-only mount.
func recordedShadow(name string, action Action, sessionsDir string) string {
	if action.Mode == ModeRO {
		return ""
	}
	id := action.RuntimeID
	if id == "" && action.Seq > 0 {
		id = runtimeIDOf(action.Seq)
	}
	if sessionsDir == "" || name == "" || id == "" {
		return ""
	}
	return filepath.Join(sessionsDir, name, "runtime", id, diskShadowFile)
}

// diskOrphans lists attached tiler volumes no active record claims.
// A foreign disk image, outside the library and outside every
// session runtime, is not a tiler view.
func diskOrphans(images []DiskImage, claimed map[string]bool, imagesDir, sessionsDir string) []Orphan {
	var orphans []Orphan
	for _, image := range images {
		point := resolvedMount(image.Point)
		if image.Point == "" || claimed[point] {
			continue
		}
		runtime := diskRuntime(image.Shadow)
		inLibrary := imagesDir != "" && underPrefix(image.Image, imagesDir)
		inSession := sessionsDir != "" && runtime != "" && underPrefix(runtime, sessionsDir)
		if !inLibrary && !inSession {
			continue
		}
		orphans = append(orphans, Orphan{
			Point:      point,
			FSType:     "dmg",
			RuntimeDir: runtime,
		})
	}
	return orphans
}

// diskRuntime is the sessions/<name>/runtime/m<seq> directory of a
// shadow path. Any other path has no runtime.
func diskRuntime(shadow string) string {
	if shadow == "" || filepath.Base(shadow) != diskShadowFile {
		return ""
	}
	runtime := filepath.Dir(filepath.Clean(shadow))
	if filepath.Base(filepath.Dir(runtime)) != "runtime" {
		return ""
	}
	return runtime
}

func claimedPoints(claimed map[string]bool) map[string]bool {
	out := make(map[string]bool, len(claimed))
	for point := range claimed {
		out[resolvedMount(point)] = true
	}
	return out
}

// samePath compares two paths. Empty stays empty: Clean would turn
// it into "." and a missing shadow would match a relative dot.
// Image and shadow paths use this spelling compare. Mount points
// use SameMountPoint, because only those are symlink-resolved.
func samePath(a, b string) bool {
	if a == "" || b == "" {
		return a == "" && b == ""
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// SameMountPoint reports whether two mount points name the same
// directory after symlink resolution. hdiutil info resolves a
// prefix such as /tmp to /private/tmp. The session record stores
// filepath.Abs, which keeps the symlink.
func SameMountPoint(a, b string) bool {
	if a == "" || b == "" {
		return a == "" && b == ""
	}
	return resolvedMount(a) == resolvedMount(b)
}

// resolvedMount is the mount point with symlinks resolved. A path
// that is not present stays the cleaned absolute spelling, so a
// torn-down mount still compares with the record.
func resolvedMount(path string) string {
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(resolved)
}

// resolveDiskPath is the reverse lookup against an hdiutil info
// snapshot. A path inside a mounted volume counts as that volume.
// A recorded mount that info no longer lists is stale.
func (e *Engine) resolveDiskPath(original, normalized string) (PathSession, error) {
	images, err := e.diskImages()
	if err != nil {
		return PathSession{}, err
	}
	if e.SessionsDir != "" && underPrefix(normalized, e.SessionsDir) &&
		hasRuntimeSegment(e.SessionsDir, normalized) {
		hit, err := e.sessionForDiskRuntime(normalized, images)
		if err != nil {
			return PathSession{}, err
		}
		hit.Path = normalized
		return hit, nil
	}
	if point, ok := longestDiskPoint(images, normalized); ok {
		hit, err := e.sessionForDiskPoint(point, images)
		if err != nil {
			return PathSession{}, err
		}
		hit.Path = normalized
		return hit, nil
	}
	hit, err := e.staleSessionForDisk(original, normalized, images)
	if err != nil {
		return PathSession{}, err
	}
	hit.Path = normalized
	return hit, nil
}

func (e *Engine) sessionForDiskPoint(point string, images []DiskImage) (PathSession, error) {
	point = filepath.Clean(point)
	rec, action, found, err := e.recordForDiskPoint(point)
	if err != nil {
		return PathSession{}, err
	}
	if !found {
		return PathSession{}, fmt.Errorf(
			"session: no session covers the mount at %s", point)
	}
	return PathSession{
		Record:     rec,
		Mountpoint: action.Mountpoint,
		Stale:      !e.diskMatch(rec.Name, action, images),
	}, nil
}

func (e *Engine) recordForDiskPoint(point string) (Record, Action, bool, error) {
	var (
		found  Record
		action Action
		hit    bool
	)
	err := e.eachRecord(func(rec Record) bool {
		if rec.State != StateActive {
			return false
		}
		for _, candidate := range rec.Actions {
			if candidate.Type == TypeMount && candidate.Status != StatusUnmounted &&
				candidate.Mountpoint != "" && SameMountPoint(candidate.Mountpoint, point) {
				found, action, hit = rec, candidate, true
				return true
			}
		}
		return false
	})
	return found, action, hit, err
}

func (e *Engine) staleSessionForDisk(original, normalized string, images []DiskImage) (PathSession, error) {
	var (
		best  PathSession
		found bool
	)
	err := e.eachRecord(func(rec Record) bool {
		if rec.State != StateActive {
			return false
		}
		for _, action := range rec.Actions {
			if !covers(action, normalized) {
				continue
			}
			if !found || len(action.Mountpoint) > len(best.Mountpoint) {
				best = PathSession{
					Record:     rec,
					Mountpoint: action.Mountpoint,
					Stale:      !e.diskMatch(rec.Name, action, images),
				}
				found = true
			}
		}
		return false
	})
	if err != nil {
		return PathSession{}, err
	}
	if !found {
		return PathSession{}, fmt.Errorf("session: no session covers %s", original)
	}
	return best, nil
}

func (e *Engine) sessionForDiskRuntime(path string, images []DiskImage) (PathSession, error) {
	name, runtimeDir, ok := sessionRuntimeOf(e.SessionsDir, path)
	if !ok {
		return PathSession{}, fmt.Errorf("session: no session covers %s", path)
	}
	runtimeDir = filepath.Clean(runtimeDir)
	for _, image := range images {
		if diskRuntime(image.Shadow) == runtimeDir {
			return e.sessionForDiskPoint(image.Point, images)
		}
	}
	return e.staleRuntimeSession(name, runtimeDir, func(string) bool { return false })
}

// longestDiskPoint is the mounted volume whose point is path or a
// directory above it, choosing the longest such point. A path inside
// the mount therefore counts as that mount.
func longestDiskPoint(images []DiskImage, path string) (string, bool) {
	best := ""
	found := false
	for _, image := range images {
		if image.Point == "" {
			continue
		}
		point := resolvedMount(image.Point)
		if !underPrefix(path, point) {
			continue
		}
		if !found || len(point) > len(best) {
			best, found = point, true
		}
	}
	return best, found
}

// openFromDisk replays mounts that hdiutil info no longer lists and
// skips a volume whose mount point, image, and shadow still match.
func (e *Engine) openFromDisk(name string, rec Record, mount MountFunc) error {
	images, err := e.diskImages()
	if err != nil {
		return err
	}
	clean := e.cleanFunc()
	for _, action := range rec.Actions {
		if action.Type != TypeMount {
			continue
		}
		if e.diskMatch(name, action, images) {
			if action.Status != StatusMounted {
				if err := e.Writeback(name, action.Seq, StatusMounted, ""); err != nil {
					return err
				}
			}
			continue
		}
		if err := e.replay(name, action, clean, mount); err != nil {
			return err
		}
	}
	if rec.State == StateClosed {
		return e.reopen(name)
	}
	return nil
}
