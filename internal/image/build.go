package image

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jiangxin/tiler/internal/config"
)

// toolOutputLimit caps how much tool output one error carries.
const toolOutputLimit = 2048

// ToolRunner executes one external tool with an argument list. The
// arguments go straight to exec, never through a shell.
type ToolRunner func(name string, args ...string) error

// FullRequest is one full image build. Callers or scene scripts
// prepare the source directory first; the build only packs it.
type FullRequest struct {
	// Config supplies the image library location and split policy.
	Config config.Config
	// Name is the explicit owner/repo image name.
	Name string
	// SrcDir is the prepared directory to pack.
	SrcDir string
	// Version and Source are optional provenance recorded in meta.
	Version string
	Source  Source
	// Builder is the tiler version stamp recorded in meta.
	Builder string
	// Refs protects referenced chains while the old image is
	// replaced. A nil Refs holds nothing.
	Refs Refs
	// Now returns the creation time. Nil means time.Now.
	Now func() time.Time
	// Run executes external tools. Nil runs mksquashfs through exec.
	Run ToolRunner
}

// FullResult reports one successful full build.
type FullResult struct {
	// ID is the first 16 hex digits of the image sha256.
	ID string
	// Path is the published active image file.
	Path string
	// SHA256 is the 64 hex digit image digest.
	SHA256 string
}

// BuildFull packs a prepared source directory into the unique active
// slot of the image library.
//
// Split storage refuses builds. The source must exist and stay
// outside every reserved tree. mksquashfs writes a synced temporary
// file inside the repository directory; the image is hashed from
// disk, the metadata is validated, and the atomic publish renames
// both into place, archiving the previous chain when it is not
// referenced. An identical active image with an empty chain is a
// no-op. Before returning, the published file is hashed again and
// compared with the stored digest. A failed packer leaves no active
// image and no temporary file behind.
func BuildFull(req FullRequest) (FullResult, error) {
	repo, err := validateFull(req)
	if err != nil {
		return FullResult{}, err
	}
	run := req.Run
	if run == nil {
		run = runTool
	}
	now := req.Now
	if now == nil {
		now = time.Now
	}
	imgTmp, err := packTemp(repo, req.SrcDir, run)
	if err != nil {
		return FullResult{}, err
	}
	res, err := finishFull(repo, req, imgTmp, now)
	if err != nil {
		removeFile(imgTmp)
		return FullResult{}, err
	}
	return res, nil
}

func validateFull(req FullRequest) (Repo, error) {
	repo, err := OpenRepo(req.Config.ImagesDir, req.Name)
	if err != nil {
		return Repo{}, err
	}
	if req.Config.SplitEnabled {
		return Repo{}, errors.New("build: split storage is enabled; full builds are refused")
	}
	if err := checkSourceDir(req.SrcDir, req.Config); err != nil {
		return Repo{}, err
	}
	return repo, nil
}

// checkSourceDir requires an existing directory outside every
// reserved tree, so the library never packs one of its own
// directories.
func checkSourceDir(src string, cfg config.Config) error {
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("build: source: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("build: source %s is not a directory", src)
	}
	if err := config.OutsideReserved(src, cfg); err != nil {
		return fmt.Errorf("build: %w", err)
	}
	return nil
}

// packTemp runs mksquashfs into a fresh temporary file inside the
// repository directory and syncs it, so the digest and the later
// rename cover the bytes that actually landed on disk.
func packTemp(repo Repo, src string, run ToolRunner) (string, error) {
	if err := os.MkdirAll(repo.Dir, 0o755); err != nil {
		return "", fmt.Errorf("build: %w", err)
	}
	name, err := tempName(fileImage)
	if err != nil {
		return "", fmt.Errorf("build: %w", err)
	}
	tmp := filepath.Join(repo.Dir, name)
	if err := packFullImage(run, src, tmp); err != nil {
		removeFile(tmp)
		return "", err
	}
	if err := syncFile(tmp); err != nil {
		removeFile(tmp)
		return "", fmt.Errorf("build: %w", err)
	}
	if err := syncDir(repo.Dir, hooks{}); err != nil {
		removeFile(tmp)
		return "", fmt.Errorf("build: %w", err)
	}
	return tmp, nil
}

// packFullImage is the mksquashfs argument list for a full image.
func packFullImage(run ToolRunner, src, dst string) error {
	return run("mksquashfs", src, dst, "-comp", "zstd", "-noappend")
}

// finishFull hashes the packed file, assembles and validates the
// metadata, publishes both, and verifies the published bytes.
func finishFull(repo Repo, req FullRequest, imgTmp string, now func() time.Time) (FullResult, error) {
	meta, err := packedMeta(repo, req, imgTmp, now)
	if err != nil {
		return FullResult{}, err
	}
	body, err := Encode(meta)
	if err != nil {
		return FullResult{}, fmt.Errorf("build: %w", err)
	}
	metaTmp, err := writeTemp(repo.Dir, fileMeta, body, hooks{})
	if err != nil {
		return FullResult{}, fmt.Errorf("build: %w", err)
	}
	if err := publishStaged(repo, meta, req.Refs, imgTmp, metaTmp, hooks{}); err != nil {
		return FullResult{}, err
	}
	if err := VerifySHA256(meta, repo.ImagePath()); err != nil {
		return FullResult{}, fmt.Errorf("build: %w", err)
	}
	return FullResult{ID: meta.ID, Path: repo.ImagePath(), SHA256: meta.SHA256}, nil
}

// packedMeta hashes the packed temporary file and assembles the full
// image metadata from the bytes that landed on disk.
func packedMeta(repo Repo, req FullRequest, tmp string, now func() time.Time) (Meta, error) {
	sum, err := fileSHA256(tmp)
	if err != nil {
		return Meta{}, fmt.Errorf("build: %w", err)
	}
	info, err := os.Stat(tmp)
	if err != nil {
		return Meta{}, fmt.Errorf("build: %w", err)
	}
	meta := Meta{
		ID:        ID(sum),
		Owner:     repo.Owner,
		Repo:      repo.Repo,
		Version:   req.Version,
		Kind:      KindFull,
		Format:    Format{Type: FormatSquashfs, Compression: CompressionZstd},
		Size:      info.Size(),
		SHA256:    hexDigest(sum),
		MtimeUnix: info.ModTime().Unix(),
		Source:    req.Source,
		CreatedAt: now().UTC(),
		Builder:   req.Builder,
	}
	if err := meta.Validate(); err != nil {
		return Meta{}, fmt.Errorf("build: %w", err)
	}
	return meta, nil
}

func hexDigest(sum [32]byte) string {
	return hex.EncodeToString(sum[:])
}

// runTool is the default ToolRunner. A non-zero exit carries the
// truncated combined output.
func runTool(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", name, err, tailOutput(out))
	}
	return nil
}

func tailOutput(out []byte) string {
	if len(out) <= toolOutputLimit {
		return strings.TrimSpace(string(out))
	}
	return "... " + strings.TrimSpace(string(out[len(out)-toolOutputLimit:]))
}

// syncFile flushes a file an external tool has written, so the
// digest and the rename only see bytes that survived a crash.
func syncFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	serr := file.Sync()
	cerr := file.Close()
	return joinWriteErr(serr, cerr)
}
