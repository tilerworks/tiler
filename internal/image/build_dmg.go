package image

import (
	"fmt"
	"os"
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
	// MountedID is the full id this shadow was attached to.
	MountedID string
	// Compression is the dmg compression to record. Empty on create
	// means ulfo. Empty on convert follows the active image.
	Compression string
	// Builder is the tiler version stamp recorded in meta.
	Builder string
	// Description is publication metadata for immutable version builds only.
	Description string
	// DescriptionSet distinguishes explicit empty text from an omitted description.
	DescriptionSet bool
	// Refs identifies sessions that pin published versions.
	Refs Refs
	// Now returns the creation time. Nil means time.Now.
	Now func() time.Time
	// Run executes hdiutil. Nil uses the platform default, which
	// calls hdiutil from PATH only on darwin. A failed constructor
	// is not executed.
	Run ToolRunner
}

// BuildDMG publishes an immutable DMG version.
func BuildDMG(req DMGRequest) (FullResult, error) {
	node, err := BuildDMGVersion(req)
	if err != nil {
		return FullResult{}, err
	}
	repo, err := OpenRepo(req.Config.ImagesDir, req.Name)
	if err != nil {
		return FullResult{}, err
	}
	path, err := repo.ObjectPayloadPath(node)
	return FullResult{ID: node.ID, Path: path, SHA256: node.SHA256}, err
}

func requireDMGCompression(comp string) error {
	if container.StoredDMGCompression(comp) {
		return nil
	}
	return fmt.Errorf("build: dmg compression %q is not allowed", comp)
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
