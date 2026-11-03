package image

import (
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jiangxin/tiler/internal/durable"

	"github.com/jiangxin/tiler/internal/config"
	"github.com/jiangxin/tiler/internal/container"
)

// toolOutputLimit caps how much tool output one error carries.
const toolOutputLimit = 2048

// ToolRunner executes one external tool with an argument list and
// extra environment variables. The arguments go straight to exec,
// never through a shell; the env entries are appended to the
// inherited environment, and an empty env changes nothing.
type ToolRunner func(name string, env []string, args ...string) error

// FullRequest is one full image build. The caller prepares the source
// directory; the build only packs it and does not modify it.
type FullRequest struct {
	// Config supplies the image library location and split policy.
	Config config.Config
	// Name is the explicit 1-to-5 segment image name.
	Name string
	// SrcDir is the prepared directory to pack.
	SrcDir string
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
	// Description fields apply only to immutable version builds.
	Description    string
	DescriptionSet bool
	// MountSmoke overrides the immutable build quality gate in tests.
	MountSmoke func(string) error
	// Diagnostics receives non-fatal validation notices. Nil suppresses notices.
	Diagnostics io.Writer
}

// FullResult reports one successful full build.
type FullResult struct {
	// ID is the lowercase hex sha256 of the image payload.
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
// outside every reserved tree. The build packs that directory as it
// stands and does not modify it. The container packer writes a synced
// temporary file inside the repository directory; the image is hashed
// from disk, the manifest
// is validated, and the atomic publish renames both into place,
// archiving the previous DAG when it is not referenced. The publish
// restarts the DAG: the new full image is the only manifest node and
// the head. An identical active image whose manifest holds no
// incremental node is a no-op. Before
// returning, the published payload is hashed again and compared with
// the stored manifest. A failed packer leaves no active image and no
// temporary file behind.
func BuildFull(req FullRequest) (FullResult, error) {
	node, err := BuildFullVersion(req)
	if err != nil {
		return FullResult{}, err
	}
	repo, err := openLoggedRepo(req.Config.ImagesDir, req.Config.LogsDir, req.Name)
	if err != nil {
		return FullResult{}, err
	}
	path, err := repo.ObjectPayloadPath(node)
	if len(node.Blocks) != 0 {
		path, err = repo.ObjectDirPath(node.ID)
	}
	return FullResult{ID: node.ID, Path: path, SHA256: node.SHA256}, err
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

func validateFull(req *FullRequest) (Repo, error) {
	repo, err := openLoggedRepo(req.Config.ImagesDir, req.Config.LogsDir, req.Name)
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
// single-node manifest, publishes both, and verifies the published
// bytes. A full build restarts the DAG: the full image is the only
// node and the head.
// packedManifest hashes the packed temporary file and assembles the
// full image manifest from the bytes that landed on disk.
func packedManifest(repo Repo, req FullRequest, tmp string, now func() time.Time) (Manifest, error) {
	sum, err := fileSHA256(tmp)
	if err != nil {
		return Manifest{}, fmt.Errorf("build: %w", err)
	}
	info, err := os.Stat(tmp)
	if err != nil {
		return Manifest{}, fmt.Errorf("build: %w", err)
	}
	node := ImageMeta{
		ID:        ID(sum),
		Kind:      KindFull,
		Size:      info.Size(),
		SHA256:    hexDigest(sum),
		MtimeUnix: info.ModTime().Unix(),
		CreatedAt: now().UTC(),
		Builder:   req.Builder,
	}
	manifest := fullManifest(
		repo.Name(),
		Format{Type: req.Format, Compression: req.Compression},
		node,
	)
	if err := manifest.Validate(); err != nil {
		return Manifest{}, fmt.Errorf("build: %w", err)
	}
	return manifest, nil
}

// fullManifest is the single-node manifest every full publish
// writes: the full image is the whole DAG and the head.
func fullManifest(name string, format Format, root ImageMeta) Manifest {
	return Manifest{
		Version: VersionManifestVersion,
		Name:    name,
		Format:  format,
		Head:    root.ID,
		Images:  []ImageMeta{root},
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
	serr := durable.Sync(file)
	cerr := file.Close()
	return joinWriteErr(serr, cerr)
}
