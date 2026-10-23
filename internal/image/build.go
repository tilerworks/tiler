package image

import (
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jiangxin/tiler/internal/config"
	"github.com/jiangxin/tiler/internal/container"
	"github.com/jiangxin/tiler/internal/gitscene"
)

// toolOutputLimit caps how much tool output one error carries.
const toolOutputLimit = 2048

// ToolRunner executes one external tool with an argument list and
// extra environment variables. The arguments go straight to exec,
// never through a shell; the env entries are appended to the
// inherited environment, and an empty env changes nothing.
type ToolRunner func(name string, env []string, args ...string) error

// FullRequest is one full image build. Callers or scene scripts
// prepare the source directory first; the build only packs it.
type FullRequest struct {
	// Config supplies the image library location and split policy.
	Config config.Config
	// Name is the explicit 1-to-5 segment image name.
	Name string
	// SrcDir is the prepared directory to pack.
	SrcDir string
	// Version and Source are optional provenance recorded in meta.
	Version string
	Source  Source
	// Builder is the tiler version stamp recorded in meta.
	Builder string
	// Format is the container type to pack, for example "squashfs"
	// or "erofs". Empty selects squashfs, so callers that omit the
	// field keep the historical default.
	Format string
	// Compression is the container compression. Empty selects the
	// format default. The registered format decides which names are
	// legal.
	Compression string
	// BlockSize, when positive, selects the blocks pipeline and is the
	// chunk size. Zero lets Config.SplitSize decide: a positive
	// SplitSize selects blocks with that size; zero keeps the
	// single-file pipeline. Tests may inject a small BlockSize
	// directly.
	BlockSize int64
	// Refs protects referenced chains while the old image is
	// replaced. A nil Refs holds nothing.
	Refs Refs
	// Now returns the creation time. Nil means time.Now.
	Now func() time.Time
	// Run executes external tools. Nil runs the container packer
	// through exec.
	Run ToolRunner
	// PrepareScene sanitizes a git source before packing. Nil uses
	// gitscene.Prepare when the source looks like a git repository;
	// non-git sources are skipped without invoking git.
	PrepareScene func(dir string) error
}

// FullResult reports one successful full build.
type FullResult struct {
	// ID is the first 16 hex digits of the image sha256.
	ID string
	// Path is the published active payload: the image file for the
	// single-file form, or the blocks directory for a blocks build.
	Path string
	// SHA256 is the 64 hex digit image digest.
	SHA256 string
}

// BuildFull packs a prepared source directory into the unique active
// slot of the image library.
//
// A positive BlockSize, or a positive Config.SplitSize when BlockSize
// is unset, selects the blocks pipeline with that chunk size. Zero
// SplitSize and BlockSize packs one image file. Production builds
// leave BlockSize zero and rely on SplitSize from configuration or
// `tiler build --split`. The source must exist and stay
// outside every reserved tree. When the source is a git repository,
// it is sanitized first and any git failure aborts the build before
// packing. The container packer writes a synced temporary file inside the
// repository directory; the image is hashed from disk, the metadata
// is validated, and the atomic publish renames both into place,
// archiving the previous chain when it is not referenced. An
// identical active image with an empty chain is a no-op. Before
// returning, the published payload is hashed again and compared with
// the stored manifest. A failed packer leaves no active image and no
// temporary file behind.
func BuildFull(req FullRequest) (FullResult, error) {
	if req.BlockSize <= 0 && req.Config.SplitSize > 0 {
		req.BlockSize = req.Config.SplitSize
	}
	if req.BlockSize > 0 {
		return buildBlocks(req)
	}
	repo, err := validateFull(&req)
	if err != nil {
		return FullResult{}, err
	}
	if err := prepareScene(req); err != nil {
		return FullResult{}, err
	}
	need, err := estimateTree(req.SrcDir)
	if err != nil {
		return FullResult{}, err
	}
	if err := ensureSpace(spaceWrite{path: repo.Dir, bytes: need}); err != nil {
		return FullResult{}, err
	}
	imgTmp, err := packTemp(repo, req.SrcDir, toolRunner(req), req.Format, req.Compression)
	if err != nil {
		return FullResult{}, err
	}
	res, err := finishFull(repo, req, imgTmp, buildNow(req))
	if err != nil {
		removeFile(imgTmp)
		return FullResult{}, err
	}
	return res, nil
}

// toolRunner is the injected runner of a request or the exec
// default.
func toolRunner(req FullRequest) ToolRunner {
	if req.Run != nil {
		return req.Run
	}
	return runTool
}

// buildNow is the injected clock of a request or time.Now.
func buildNow(req FullRequest) func() time.Time {
	if req.Now != nil {
		return req.Now
	}
	return time.Now
}

// prepareScene sanitizes the source when it is a git repository. A
// non-git directory is skipped without invoking git.
func prepareScene(req FullRequest) error {
	if !gitscene.IsGit(req.SrcDir) {
		return nil
	}
	prepare := req.PrepareScene
	if prepare == nil {
		prepare = gitscene.Prepare
	}
	if err := prepare(req.SrcDir); err != nil {
		return fmt.Errorf("build: %w", err)
	}
	return nil
}

func validateFull(req *FullRequest) (Repo, error) {
	repo, err := OpenRepo(req.Config.ImagesDir, req.Name)
	if err != nil {
		return Repo{}, err
	}
	if err := checkNamePlacement(req.Config.ImagesDir, repo.Segments); err != nil {
		return Repo{}, err
	}
	if err := checkSourceDir(req.SrcDir, req.Config); err != nil {
		return Repo{}, err
	}
	if err := resolveFullRequest(req); err != nil {
		return Repo{}, err
	}
	return repo, nil
}

// resolveFullRequest fills the container type and compression used
// to pack and to record meta. An empty type selects squashfs. An
// empty compression selects that format's default.
func resolveFullRequest(req *FullRequest) error {
	typ := req.Format
	if typ == "" {
		typ = FormatSquashfs
	}
	format, err := container.Lookup(typ)
	if err != nil {
		return fmt.Errorf("build: %w", err)
	}
	compression := req.Compression
	if compression == "" {
		compression = format.DefaultCompression()
	}
	if err := acceptCompression(format, compression); err != nil {
		return err
	}
	req.Format = format.Type()
	req.Compression = compression
	return nil
}

// acceptStoredContainer checks a container recorded on metadata.
// Incremental builds inherit this pair and refuse a type or
// compression the registry does not allow.
func acceptStoredContainer(format Format) error {
	f, err := container.Lookup(format.Type)
	if err != nil {
		return fmt.Errorf("build: %w", err)
	}
	return acceptCompression(f, format.Compression)
}

// acceptCompression reports a compression the format refuses. An
// empty compression is refused; callers that want the default fill
// it in before calling.
func acceptCompression(f container.Format, compression string) error {
	if compression == "" || !f.AllowsCompression(compression) {
		return fmt.Errorf("build: %s compression %q is not allowed", f.Type(), compression)
	}
	return nil
}

// checkSourceDir requires an existing directory outside every
// reserved tree and not covering one, so the library never packs
// its own images, sessions, or cache into a new image.
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
	if err := config.CoversReserved(src, cfg); err != nil {
		return fmt.Errorf("build: %w", err)
	}
	return nil
}

// packTemp runs the container packer into a fresh temporary file
// inside the repository directory and syncs it, so the digest and
// the later rename cover the bytes that actually landed on disk.
func packTemp(repo Repo, src string, run ToolRunner, typ, compression string) (string, error) {
	if err := os.MkdirAll(repo.Dir, 0o755); err != nil {
		return "", fmt.Errorf("build: %w", err)
	}
	name, err := tempName(fileImage)
	if err != nil {
		return "", fmt.Errorf("build: %w", err)
	}
	tmp := filepath.Join(repo.Dir, name)
	if err := packFullImage(run, src, tmp, typ, compression); err != nil {
		removeFile(tmp)
		return "", failTool(tmp, err)
	}
	if err := syncFile(tmp); err != nil {
		removeFile(tmp)
		return "", failWrite(tmp, err)
	}
	if err := syncDir(repo.Dir, hooks{}); err != nil {
		removeFile(tmp)
		return "", fmt.Errorf("build: %w", err)
	}
	return tmp, nil
}

// packFullImage packs a full image through the registered container.
// The format pins the creation timestamp so the same tree packs to
// the same bytes.
func packFullImage(run ToolRunner, src, dst, typ, compression string) error {
	return packContainer(run, src, dst, typ, compression, false)
}

// packContainer looks up typ and packs src to dst. incr selects the
// incremental packer, which keeps whiteouts when the format stores
// them.
func packContainer(run ToolRunner, src, dst, typ, compression string, incr bool) error {
	format, err := container.Lookup(typ)
	if err != nil {
		return fmt.Errorf("build: %w", err)
	}
	runner := container.ToolRunner(run)
	if incr {
		return format.PackIncr(runner, src, dst, compression)
	}
	return format.PackFull(runner, src, dst, compression)
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
	meta := newFullMeta(repo, req, now)
	meta.ID = ID(sum)
	meta.Size = info.Size()
	meta.SHA256 = hexDigest(sum)
	meta.MtimeUnix = info.ModTime().Unix()
	if err := meta.Validate(); err != nil {
		return Meta{}, fmt.Errorf("build: %w", err)
	}
	return meta, nil
}

// newFullMeta is the metadata skeleton every full build shares
// before the payload facts are filled in.
func newFullMeta(repo Repo, req FullRequest, now func() time.Time) Meta {
	return Meta{
		Name:      repo.Name(),
		Version:   req.Version,
		Kind:      KindFull,
		Format:    Format{Type: req.Format, Compression: req.Compression},
		Source:    req.Source,
		CreatedAt: now().UTC(),
		Builder:   req.Builder,
	}
}

func hexDigest(sum [32]byte) string {
	return hex.EncodeToString(sum[:])
}

// runTool is the default ToolRunner. A non-zero exit carries the
// truncated combined output.
func runTool(name string, env []string, args ...string) error {
	cmd := exec.Command(name, args...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	out, err := cmd.CombinedOutput()
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
