package image

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/jiangxin/tiler/internal/config"
	"github.com/jiangxin/tiler/internal/container"
)

// linkNow supplies CreatedAt for LinkExternal. Tests may replace it.
var linkNow = func() time.Time { return time.Now().UTC() }

// linkDarwinHost reports whether LinkExternal applies the macOS
// format policy. Tests may replace it.
var linkDarwinHost = func() bool { return runtime.GOOS == "darwin" }

// LinkExternal registers an external image file in the library leaf
// name under imagesDir. It writes an absolute symlink to
// image.sqsh or image.dmg and meta.yaml without copying payload
// bytes. The same external target and digest is idempotent.
func LinkExternal(imagesDir, name, srcPath, builder string) error {
	repo, err := OpenRepo(imagesDir, name)
	if err != nil {
		return fmt.Errorf("link: %w", err)
	}
	absSrc, err := filepath.Abs(srcPath)
	if err != nil {
		return fmt.Errorf("link: %w", err)
	}
	if err := validateLinkSource(absSrc, imagesDir); err != nil {
		return err
	}
	typ, comp, err := detectLinkFormat(absSrc)
	if err != nil {
		return err
	}
	if err := refuseLinkFormat(typ); err != nil {
		return err
	}
	sum, size, mtime, err := digestRegularFile(absSrc)
	if err != nil {
		return fmt.Errorf("link: %w", err)
	}
	next := linkMeta(repo, typ, comp, sum, size, mtime, builder)
	idle, err := checkLinkIdle(repo, absSrc, next)
	if err != nil {
		return err
	}
	if idle {
		return rebuildIndexes(repo)
	}
	payloadPath := filepath.Join(repo.Dir, activeImageName(typ))
	if err := os.MkdirAll(repo.Dir, 0o755); err != nil {
		return fmt.Errorf("link: %w", err)
	}
	if err := writeLinkSymlink(absSrc, payloadPath); err != nil {
		return err
	}
	if err := writeLinkMeta(repo, next, hooks{}); err != nil {
		removeFile(payloadPath)
		return err
	}
	return rebuildIndexes(repo)
}

func validateLinkSource(absSrc, imagesDir string) error {
	info, err := os.Stat(absSrc)
	if err != nil {
		return fmt.Errorf("link: source: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("link: source %s is not a regular file", absSrc)
	}
	cfg, cfgErr := linkConfig(imagesDir)
	if cfgErr == nil {
		if err := config.OutsideReserved(absSrc, cfg); err != nil {
			return fmt.Errorf("link: %w", err)
		}
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("link: %w", cfgErr)
	}
	fallback := config.Defaults(home)
	fallback.ImagesDir = filepath.Clean(imagesDir)
	if err := config.OutsideReserved(absSrc, fallback); err != nil {
		return fmt.Errorf("link: %w", err)
	}
	return nil
}

func linkConfig(imagesDir string) (config.Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return config.Config{}, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return config.Config{}, err
	}
	dir := filepath.Clean(imagesDir)
	return config.Load(cwd, home, "/", "", config.Patch{
		ImagesDir: &dir,
	})
}

func detectLinkFormat(path string) (string, string, error) {
	f, err := container.DetectFile(path)
	if err == nil {
		return f.Type(), f.DefaultCompression(), nil
	}
	ok, kerr := container.ReadKolyTrailer(path)
	if kerr != nil {
		return "", "", fmt.Errorf("link: detect: %w", kerr)
	}
	if ok {
		d := container.DMG()
		return d.Type(), d.DefaultCompression(), nil
	}
	if errors.Is(err, container.ErrNotDetected) {
		return "", "", fmt.Errorf("link: %w", err)
	}
	return "", "", fmt.Errorf("link: %w", err)
}

func refuseLinkFormat(typ string) error {
	if linkDarwinHost() {
		if typ == FormatSquashfs || typ == FormatErofs {
			return fmt.Errorf("link: %s is not available on macOS; use dmg", typ)
		}
		return nil
	}
	if typ == FormatDMG {
		return fmt.Errorf("link: dmg is not available on this host")
	}
	return nil
}

func digestRegularFile(path string) ([32]byte, int64, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return [32]byte{}, 0, 0, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return [32]byte{}, 0, 0, err
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return [32]byte{}, 0, 0, err
	}
	var sum [32]byte
	copy(sum[:], hash.Sum(nil))
	return sum, info.Size(), info.ModTime().Unix(), nil
}

func linkMeta(repo Repo, typ, comp string, sum [32]byte, size, mtime int64, builder string) Meta {
	digest := hex.EncodeToString(sum[:])
	now := linkNow().UTC()
	return Meta{
		ID:        digest,
		Name:      repo.Name(),
		Kind:      KindFull,
		Format:    Format{Type: typ, Compression: comp},
		Size:      size,
		SHA256:    digest,
		MtimeUnix: mtime,
		CreatedAt: now,
		Builder:   builder,
	}
}

func checkLinkIdle(repo Repo, absSrc string, next Meta) (bool, error) {
	if err := next.Validate(); err != nil {
		return false, fmt.Errorf("link: %w", err)
	}
	existing, exists, err := linkExisting(repo)
	if err != nil {
		return false, err
	}
	if !exists {
		if err := checkStrayPayload(repo.Dir, absSrc, next); err != nil {
			return false, err
		}
		return false, nil
	}
	if len(existing.Chain) > 0 {
		return false, fmt.Errorf("link: active image has incremental chain")
	}
	if existing.SHA256 == next.SHA256 && existing.Size == next.Size {
		target, ok, err := readLinkTarget(repo, next.Format.Type)
		if err != nil {
			return false, err
		}
		if ok && target == absSrc {
			return true, nil
		}
	}
	return false, linkConflict(repo, absSrc, existing, next)
}

func linkExisting(repo Repo) (Meta, bool, error) {
	meta, err := Load(repo.MetaPath())
	if errors.Is(err, os.ErrNotExist) {
		return Meta{}, false, nil
	}
	if err != nil {
		return Meta{}, false, err
	}
	return meta, true, nil
}

func checkStrayPayload(dir, absSrc string, next Meta) error {
	payloadPath := filepath.Join(dir, activeImageName(next.Format.Type))
	info, err := os.Lstat(payloadPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(payloadPath)
		if err != nil {
			return err
		}
		resolved, err := filepath.Abs(target)
		if err != nil {
			return err
		}
		if resolved == absSrc {
			return nil
		}
	}
	return fmt.Errorf("link: leaf payload conflicts with external link")
}

func readLinkTarget(repo Repo, typ string) (string, bool, error) {
	path := filepath.Join(repo.Dir, activeImageName(typ))
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return "", false, nil
	}
	target, err := os.Readlink(path)
	if err != nil {
		return "", false, err
	}
	if !filepath.IsAbs(target) {
		return "", true, fmt.Errorf("link: active payload symlink is not absolute")
	}
	return target, true, nil
}

func linkConflict(repo Repo, absSrc string, existing, next Meta) error {
	target, ok, err := readLinkTarget(repo, existing.Format.Type)
	if err != nil {
		return err
	}
	if ok && target != absSrc {
		return fmt.Errorf("link: active payload points elsewhere")
	}
	if !ok {
		return fmt.Errorf("link: active payload is not an external symlink")
	}
	if existing.SHA256 != next.SHA256 || existing.Size != next.Size {
		return fmt.Errorf("link: active meta does not match external file")
	}
	return fmt.Errorf("link: active image conflicts with external link")
}

func writeLinkSymlink(absSrc, payloadPath string) error {
	info, err := os.Lstat(payloadPath)
	if errors.Is(err, os.ErrNotExist) {
		return os.Symlink(absSrc, payloadPath)
	}
	if err != nil {
		return fmt.Errorf("link: %w", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("link: active payload is not an external symlink")
	}
	target, err := os.Readlink(payloadPath)
	if err != nil {
		return fmt.Errorf("link: %w", err)
	}
	if target == absSrc {
		return nil
	}
	return fmt.Errorf("link: active payload points elsewhere")
}

func writeLinkMeta(repo Repo, meta Meta, seam hooks) error {
	body, err := Encode(meta)
	if err != nil {
		return fmt.Errorf("link: %w", err)
	}
	metaTmp, err := writeTemp(repo.Dir, fileMeta, body, seam)
	if err != nil {
		return fmt.Errorf("link: %w", err)
	}
	if err := seam.do(metaTmp, repo.MetaPath()); err != nil {
		undoLanded(metaTmp, repo.MetaPath())
		removeFile(metaTmp)
		return fmt.Errorf("link: %w", err)
	}
	return nil
}
