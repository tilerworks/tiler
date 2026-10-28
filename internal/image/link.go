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

// linkSymlink creates a symlink. Tests may replace it to force the
// path-ref fallback when the host cannot create symlinks.
var linkSymlink = os.Symlink

// LinkExternal registers an external image file in the library leaf
// name under imagesDir. It writes an absolute symlink to
// image.sqsh or image.dmg when possible, otherwise a path-ref
// regular file whose content is the absolute source path, plus
// meta.yaml, without copying payload bytes. The same external
// target and digest is idempotent.
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
	if err := writeLinkExternal(absSrc, payloadPath); err != nil {
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

func linkMeta(repo Repo, typ, comp string, sum [32]byte, size, mtime int64, builder string) Manifest {
	digest := hex.EncodeToString(sum[:])
	now := linkNow().UTC()
	return fullManifest(repo.Name(), Format{Type: typ, Compression: comp}, ImageMeta{
		ID:        digest,
		Kind:      KindFull,
		Size:      size,
		SHA256:    digest,
		MtimeUnix: mtime,
		CreatedAt: now,
		Builder:   builder,
	})
}

func checkLinkIdle(repo Repo, absSrc string, next Manifest) (bool, error) {
	if err := next.Validate(); err != nil {
		return false, fmt.Errorf("link: %w", err)
	}
	nextRoot, err := next.Full()
	if err != nil {
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
	existingRoot, err := existing.Full()
	if err != nil {
		return false, fmt.Errorf("link: %w", err)
	}
	if existingRoot.SHA256 == nextRoot.SHA256 && existingRoot.Size == nextRoot.Size {
		target, ok, err := readLinkTarget(repo, next.Format.Type)
		if err != nil {
			return false, err
		}
		// Re-registering the same external file is a no-op even when
		// incremental layers sit on top: the idle path keeps meta,
		// DAG, and leaf untouched, so only a payload *replacing*
		// link has to clear the branch guard below.
		if ok && target == absSrc {
			return true, nil
		}
	}
	if len(existing.Images) > 1 {
		return false, fmt.Errorf("link: active image has incremental chain")
	}
	return false, linkConflict(repo, absSrc, existing, next)
}

func linkExisting(repo Repo) (Manifest, bool, error) {
	manifest, err := LoadManifest(repo.MetaPath())
	if errors.Is(err, os.ErrNotExist) {
		return Manifest{}, false, nil
	}
	if err != nil {
		return Manifest{}, false, err
	}
	return manifest, true, nil
}

func checkStrayPayload(dir, absSrc string, next Manifest) error {
	payloadPath := filepath.Join(dir, activeImageName(next.Format.Type))
	target, ok, err := readExternalLinkTarget(payloadPath)
	if err != nil {
		return err
	}
	if !ok {
		if _, err := os.Lstat(payloadPath); errors.Is(err, os.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
		return fmt.Errorf("link: leaf payload conflicts with external link")
	}
	if target == absSrc {
		return nil
	}
	return fmt.Errorf("link: leaf payload conflicts with external link")
}

func readLinkTarget(repo Repo, typ string) (string, bool, error) {
	path := filepath.Join(repo.Dir, activeImageName(typ))
	target, ok, err := readExternalLinkTarget(path)
	if err != nil {
		return "", ok, fmt.Errorf("link: %w", err)
	}
	return target, ok, nil
}

func linkConflict(repo Repo, absSrc string, existing, next Manifest) error {
	target, ok, err := readLinkTarget(repo, existing.Format.Type)
	if err != nil {
		return err
	}
	if ok && target != absSrc {
		return fmt.Errorf("link: active payload points elsewhere")
	}
	if !ok {
		return fmt.Errorf("link: active payload is not an external link")
	}
	existingRoot, err := existing.Full()
	if err != nil {
		return fmt.Errorf("link: %w", err)
	}
	nextRoot, err := next.Full()
	if err != nil {
		return fmt.Errorf("link: %w", err)
	}
	if existingRoot.SHA256 != nextRoot.SHA256 || existingRoot.Size != nextRoot.Size {
		return fmt.Errorf("link: active meta does not match external file")
	}
	return fmt.Errorf("link: active image conflicts with external link")
}

// writeLinkExternal records absSrc at payloadPath as a symlink when
// possible, otherwise as a path-ref regular file. An existing link
// that already names absSrc is left alone.
func writeLinkExternal(absSrc, payloadPath string) error {
	target, ok, err := readExternalLinkTarget(payloadPath)
	if err != nil {
		return fmt.Errorf("link: %w", err)
	}
	if ok {
		if target == absSrc {
			return nil
		}
		return fmt.Errorf("link: active payload points elsewhere")
	}
	if _, err := os.Lstat(payloadPath); err == nil {
		return fmt.Errorf("link: active payload is not an external link")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("link: %w", err)
	}
	if err := linkSymlink(absSrc, payloadPath); err == nil {
		return nil
	}
	if err := writePathRef(absSrc, payloadPath); err != nil {
		return fmt.Errorf("link: %w", err)
	}
	return nil
}

func writeLinkMeta(repo Repo, manifest Manifest, seam hooks) error {
	body, err := EncodeManifest(manifest)
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
