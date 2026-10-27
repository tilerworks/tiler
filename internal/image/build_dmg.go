package image

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jiangxin/tiler/internal/config"
	"github.com/jiangxin/tiler/internal/container"
)

// DMGRequest is one disk-image publish. SrcDir selects hdiutil
// create of a plain directory. Shadow selects hdiutil convert of
// the active image.dmg with that session shadow. Exactly one is set.
// The shadow file is not renamed, copied, or removed.
type DMGRequest struct {
	// Config supplies the image library location.
	Config config.Config
	// Name is the explicit 1-to-5 segment image name.
	Name string
	// SrcDir is the directory hdiutil create packs. Empty for a bake.
	SrcDir string
	// Shadow is sessions/<name>/runtime/m<seq>/upper.shadow. Empty
	// for a directory create. The file is not renamed or removed.
	// Its post-convert digest is the bake content fingerprint.
	Shadow string
	// MountedID is the full id this shadow was attached to. When
	// _hold/<id>/image.dmg exists, convert reads that file instead of
	// the active image.dmg. Empty selects a held copy Refs still
	// pins once the active id is a newer image.
	MountedID string
	// Compression is the dmg compression to record. Empty on create
	// means ulfo. Empty on convert follows the active image.
	Compression string
	// Builder is the tiler version stamp recorded in meta.
	Builder string
	// Refs protects a referenced image while it is replaced. A nil
	// Refs holds nothing, so the previous image is archived.
	Refs Refs
	// Now returns the creation time. Nil means time.Now.
	Now func() time.Time
	// Run executes hdiutil. Nil uses the platform default, which
	// calls hdiutil from PATH only on darwin. A failed constructor
	// is not executed.
	Run ToolRunner
}

// BuildDMG publishes one image.dmg. A directory create hashes the
// temp file hdiutil wrote and stores meta beside it. A shadow bake
// converts the image that shadow was attached to. The first bake
// reads the active image.dmg. After publish moves a pinned base
// into hold, a later bake reads that held copy and leaves the
// shadow file where it is. A failed create or convert deletes only
// the temp file. The active image and the shadow stay as they were.
func BuildDMG(req DMGRequest) (FullResult, error) {
	if req.SrcDir != "" && req.Shadow != "" {
		return FullResult{}, errors.New("build: dmg source and shadow are both set")
	}
	if req.SrcDir == "" && req.Shadow == "" {
		return FullResult{}, errors.New("build: dmg build needs a source directory or a shadow")
	}
	repo, err := OpenRepo(req.Config.ImagesDir, req.Name)
	if err != nil {
		return FullResult{}, fmt.Errorf("build: %w", err)
	}
	if err := checkNamePlacement(req.Config.ImagesDir, repo.Segments); err != nil {
		return FullResult{}, err
	}
	if req.Shadow != "" {
		return bakeDMG(repo, req)
	}
	return createDMG(repo, req)
}

func createDMG(repo Repo, req DMGRequest) (FullResult, error) {
	if err := checkSourceDir(req.SrcDir, req.Config); err != nil {
		return FullResult{}, err
	}
	comp := req.Compression
	if comp == "" {
		comp = container.DMG().DefaultCompression()
	}
	if err := requireDMGCompression(comp); err != nil {
		return FullResult{}, err
	}
	content, err := dirContentSHA256(req.SrcDir)
	if err != nil {
		return FullResult{}, fmt.Errorf("build: %w", err)
	}
	if idle, err := idleDMG(repo, req, content, comp); err != nil || idle.ID != "" {
		return idle, err
	}
	need, err := estimateTree(req.SrcDir)
	if err != nil {
		return FullResult{}, err
	}
	if err := ensureSpace(spaceWrite{path: repo.Dir, bytes: need}); err != nil {
		return FullResult{}, err
	}
	tmp, err := dmgTemp(repo)
	if err != nil {
		return FullResult{}, err
	}
	vol := repo.Segments[len(repo.Segments)-1]
	argv, consErr := container.HdiutilCreateArgv(req.SrcDir, comp, vol, tmp)
	if err := runDMG(req, argv, consErr, tmp); err != nil {
		return FullResult{}, err
	}
	return finishDMG(repo, req, tmp, comp, content)
}

func bakeDMG(repo Repo, req DMGRequest) (FullResult, error) {
	active, activePath, err := activeDMG(repo)
	if err != nil {
		return FullResult{}, err
	}
	base, baseComp, err := bakeSource(repo, active, activePath, req)
	if err != nil {
		return FullResult{}, err
	}
	base, err = ResolvePayloadBytesPath(base)
	if err != nil {
		return FullResult{}, fmt.Errorf("build: %w", err)
	}
	comp := req.Compression
	if comp == "" {
		comp = baseComp
	}
	if err := requireDMGCompression(comp); err != nil {
		return FullResult{}, err
	}
	// Idle compares the live shadow to the digest stored after the
	// last successful convert. Walking the mount view is unnecessary:
	// only the shadow carries bake deltas.
	content, err := shadowContentSHA256(req.Shadow)
	if err != nil {
		return FullResult{}, fmt.Errorf("build: %w", err)
	}
	if idle, err := idleDMG(repo, req, content, comp); err != nil || idle.ID != "" {
		return idle, err
	}
	need, err := estimateFiles(base, req.Shadow)
	if err != nil {
		return FullResult{}, err
	}
	if err := ensureSpace(spaceWrite{path: repo.Dir, bytes: need}); err != nil {
		return FullResult{}, err
	}
	tmp, err := dmgTemp(repo)
	if err != nil {
		return FullResult{}, err
	}
	// An empty request compression follows the base inside the
	// constructor. The shadow path is only an argument.
	argv, consErr := container.HdiutilConvertArgv(
		base, req.Compression, baseComp, req.Shadow, tmp)
	if err := runDMG(req, argv, consErr, tmp); err != nil {
		return FullResult{}, err
	}
	// convert may rewrite the shadow; record the post-convert bytes.
	content, err = shadowContentSHA256(req.Shadow)
	if err != nil {
		removeFile(tmp)
		return FullResult{}, fmt.Errorf("build: %w", err)
	}
	return finishDMG(repo, req, tmp, comp, content)
}

// idleDMG returns the active image when its logical content already
// matches. preparePublish still runs so unreferenced holds archive.
func idleDMG(repo Repo, req DMGRequest, content, compression string) (FullResult, error) {
	active, exists, err := loadActiveManifest(repo)
	if err != nil {
		return FullResult{}, fmt.Errorf("build: %w", err)
	}
	if !exists || !dmgContentIdle(active, content, compression) {
		return FullResult{}, nil
	}
	if _, _, err := preparePublish(repo, req.Refs, hooks{}, ""); err != nil {
		return FullResult{}, err
	}
	root, err := active.Full()
	if err != nil {
		return FullResult{}, fmt.Errorf("build: %w", err)
	}
	return FullResult{
		ID:     root.ID,
		Path:   filepath.Join(repo.Dir, ImageDMG),
		SHA256: root.SHA256,
	}, nil
}

// bakeSource is the disk image the live shadow belongs to. Publish
// moves a pinned active image.dmg into _hold/<id> and puts the new
// bytes at the active slot. The shadow stays the delta for the held
// base, so a later bake must not open the new active file.
func bakeSource(repo Repo, active Manifest, activePath string, req DMGRequest) (string, string, error) {
	root, err := active.Full()
	if err != nil {
		return "", "", fmt.Errorf("build: %w", err)
	}
	if req.MountedID != "" && req.MountedID != root.ID {
		path, comp, found, err := holdDMG(repo, req.MountedID)
		if err != nil {
			return "", "", err
		}
		if found {
			return path, comp, nil
		}
	}
	pinsActive, err := heldBy(req.Refs, refFor(repo, root.ID))
	if err != nil {
		return "", "", fmt.Errorf("build: %w", err)
	}
	if !pinsActive {
		path, comp, found, err := pinnedHoldDMG(repo, req.Refs)
		if err != nil {
			return "", "", err
		}
		if found {
			return path, comp, nil
		}
	}
	return activePath, active.Format.Compression, nil
}

// holdDMG is _hold/<id>/image.dmg when that held copy exists.
func holdDMG(repo Repo, id string) (string, string, bool, error) {
	hold, err := repo.HoldPath(id)
	if err != nil {
		return "", "", false, fmt.Errorf("build: %w", err)
	}
	path := filepath.Join(hold, ImageDMG)
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", "", false, nil
		}
		return "", "", false, fmt.Errorf("build: %w", err)
	}
	manifest, err := LoadManifest(filepath.Join(hold, fileMeta))
	if err != nil {
		return "", "", false, fmt.Errorf("build: %w", err)
	}
	if manifest.Format.Type != FormatDMG {
		return "", "", false, nil
	}
	return path, manifest.Format.Compression, true, nil
}

// pinnedHoldDMG is the one held dmg Refs still pins. Several pinned
// holds are ambiguous without the session id of this shadow.
func pinnedHoldDMG(repo Repo, refs Refs) (string, string, bool, error) {
	ids, err := listHoldIDs(repo)
	if err != nil {
		return "", "", false, fmt.Errorf("build: %w", err)
	}
	var (
		path string
		comp string
		hit  bool
	)
	for _, id := range ids {
		ok, err := heldBy(refs, refFor(repo, id))
		if err != nil {
			return "", "", false, fmt.Errorf("build: %w", err)
		}
		if !ok {
			continue
		}
		held, heldComp, found, err := holdDMG(repo, id)
		if err != nil {
			return "", "", false, err
		}
		if !found {
			continue
		}
		if hit {
			return "", "", false, errors.New("build: more than one held image matches this shadow")
		}
		path, comp, hit = held, heldComp, true
	}
	return path, comp, hit, nil
}

func activeDMG(repo Repo) (Manifest, string, error) {
	active, exists, err := loadActiveManifest(repo)
	if err != nil {
		return Manifest{}, "", fmt.Errorf("build: %w", err)
	}
	if !exists {
		return Manifest{}, "", errors.New("build: no active image to bake")
	}
	if active.Format.Type != FormatDMG {
		return Manifest{}, "", fmt.Errorf("build: active image is %s, not dmg", active.Format.Type)
	}
	return active, filepath.Join(repo.Dir, ImageDMG), nil
}

func requireDMGCompression(comp string) error {
	if container.StoredDMGCompression(comp) {
		return nil
	}
	return fmt.Errorf("build: dmg compression %q is not allowed", comp)
}

func dmgTemp(repo Repo) (string, error) {
	if err := os.MkdirAll(repo.Dir, 0o755); err != nil {
		return "", fmt.Errorf("build: %w", err)
	}
	name, err := tempName(ImageDMG)
	if err != nil {
		return "", fmt.Errorf("build: %w", err)
	}
	// The name ends in .dmg so hdiutil does not append a second suffix.
	return filepath.Join(repo.Dir, name+".dmg"), nil
}

// runDMG executes one constructor vector. A constructor error is
// returned without calling the runner. Any failure removes the temp
// file and nothing else.
func runDMG(req DMGRequest, argv []string, consErr error, tmp string) error {
	run := req.Run
	if run == nil {
		run = defaultDiskTool
	}
	if err := container.RunHdiutil(container.ToolRunner(run), argv, consErr); err != nil {
		removeFile(tmp)
		return fmt.Errorf("build: %w", err)
	}
	return nil
}

func finishDMG(repo Repo, req DMGRequest, tmp, compression, content string) (FullResult, error) {
	if err := syncFile(tmp); err != nil {
		removeFile(tmp)
		return FullResult{}, failWrite(tmp, err)
	}
	if err := syncDir(repo.Dir, hooks{}); err != nil {
		removeFile(tmp)
		return FullResult{}, fmt.Errorf("build: %w", err)
	}
	manifest, err := dmgManifest(repo, req, tmp, compression, content)
	if err != nil {
		removeFile(tmp)
		return FullResult{}, err
	}
	body, err := EncodeManifest(manifest)
	if err != nil {
		removeFile(tmp)
		return FullResult{}, fmt.Errorf("build: %w", err)
	}
	metaTmp, err := writeTemp(repo.Dir, fileMeta, body, hooks{})
	if err != nil {
		removeFile(tmp)
		return FullResult{}, fmt.Errorf("build: %w", err)
	}
	if err := publishStaged(repo, manifest, req.Refs, tmp, metaTmp, hooks{}); err != nil {
		return FullResult{}, err
	}
	root, err := manifest.Full()
	if err != nil {
		return FullResult{}, fmt.Errorf("build: %w", err)
	}
	path := filepath.Join(repo.Dir, ImageDMG)
	if err := verifyFileSHA256(path, root.SHA256); err != nil {
		return FullResult{}, fmt.Errorf("build: %w", err)
	}
	return FullResult{ID: root.ID, Path: path, SHA256: root.SHA256}, nil
}

// dmgManifest assembles the single-node manifest of a dmg publish.
// The content fingerprint marks idle rebuilds; see DMGRequest.Shadow.
func dmgManifest(repo Repo, req DMGRequest, tmp, compression, content string) (Manifest, error) {
	sum, err := fileSHA256(tmp)
	if err != nil {
		return Manifest{}, fmt.Errorf("build: %w", err)
	}
	info, err := os.Stat(tmp)
	if err != nil {
		return Manifest{}, fmt.Errorf("build: %w", err)
	}
	now := req.Now
	if now == nil {
		now = time.Now
	}
	node := ImageMeta{
		ID:            ID(sum),
		Kind:          KindFull,
		Size:          info.Size(),
		SHA256:        hexDigest(sum),
		MtimeUnix:     info.ModTime().Unix(),
		CreatedAt:     now().UTC(),
		Builder:       req.Builder,
		ContentSHA256: content,
	}
	manifest := fullManifest(
		repo.Name(),
		Format{Type: FormatDMG, Compression: compression},
		node,
	)
	if err := manifest.Validate(); err != nil {
		return Manifest{}, fmt.Errorf("build: %w", err)
	}
	return manifest, nil
}
