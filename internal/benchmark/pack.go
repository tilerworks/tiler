package benchmark

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/jiangxin/tiler/internal/container"
)

// Pack writes image from src. typ is dmg, tar, squashfs, or erofs.
// compress is the canonical token from Plan. An empty erofs compress
// is chosen from mkfs.erofs --help via the command. Plan already
// rejects a type the host cannot run; Pack still builds the vector
// so a fake command can record it on any OS.
func Pack(cmd Command, typ, src, image, compress string) error {
	if err := needCommand(cmd); err != nil {
		return err
	}
	switch typ {
	case "dmg":
		return packDMG(cmd, src, image, compress)
	case "tar":
		return packTar(cmd, src, image)
	case "squashfs":
		return packFormat(cmd, typ, src, image, compress)
	case "erofs":
		return packErofs(cmd, src, image, compress)
	default:
		return fmt.Errorf("benchmark: unknown image type %q", typ)
	}
}

func packDMG(cmd Command, src, image, compress string) error {
	argv, err := container.HdiutilCreateArgv(src, compress, filepath.Base(image), image)
	if err != nil {
		return err
	}
	if err := container.RunHdiutil(toolRunner(cmd), argv, nil); err != nil {
		return fmt.Errorf("benchmark: pack dmg: %w", err)
	}
	return nil
}

func packFormat(cmd Command, typ, src, image, compress string) error {
	format, err := container.Lookup(typ)
	if err != nil {
		return fmt.Errorf("benchmark: %w", err)
	}
	err = format.PackFull(toolRunner(cmd), src, image, compress)
	if err != nil {
		return fmt.Errorf("benchmark: pack %s: %w", typ, err)
	}
	return nil
}

func packErofs(cmd Command, src, image, compress string) error {
	chosen, err := erofsCompress(cmd, compress)
	if err != nil {
		return err
	}
	return packFormat(cmd, "erofs", src, image, chosen)
}

// erofsCompress keeps an explicit token. An empty token is the
// compressor PickErofsCompression selects from mkfs.erofs --help.
// That help text often exits non-zero after printing the list, so
// a non-empty stdout is still parsed.
func erofsCompress(cmd Command, compress string) (string, error) {
	if compress != "" {
		return compress, nil
	}
	out, err := cmd("mkfs.erofs", nil, "--help")
	text := strings.TrimSpace(string(out))
	if text == "" {
		return "", helpErr(err)
	}
	chosen, pickErr := container.PickErofsCompression(container.ErofsCompressors(text))
	if pickErr != nil {
		return "", helpPickErr(pickErr, err)
	}
	return chosen, nil
}

func helpErr(err error) error {
	if err != nil {
		return fmt.Errorf("benchmark: mkfs.erofs --help: %w", err)
	}
	return errors.New("benchmark: mkfs.erofs --help returned no text")
}

func helpPickErr(pickErr, runErr error) error {
	if runErr != nil {
		return fmt.Errorf("benchmark: mkfs.erofs --help: %w", errors.Join(pickErr, runErr))
	}
	return fmt.Errorf("benchmark: %w", pickErr)
}

func packTar(cmd Command, src, image string) error {
	if strings.TrimSpace(src) == "" || strings.TrimSpace(image) == "" {
		return errors.New("benchmark: tar source and archive paths are required")
	}
	src = filepath.Clean(src)
	image = filepath.Clean(image)
	if err := rejectArchiveInside(src, image); err != nil {
		return err
	}
	_, err := cmd("tar", nil, "-czf", image, "-C", filepath.Dir(src), filepath.Base(src))
	if err != nil {
		return fmt.Errorf("benchmark: pack tar: %w", err)
	}
	return nil
}

func rejectArchiveInside(src, image string) error {
	rel, err := filepath.Rel(src, image)
	if err != nil {
		return fmt.Errorf("benchmark: archive path: %w", err)
	}
	if !archiveInSource(rel) {
		return nil
	}
	return fmt.Errorf("benchmark: archive %s is inside the source tree %s", image, src)
}

func archiveInSource(rel string) bool {
	if rel == "." {
		return true
	}
	prefix := ".." + string(filepath.Separator)
	if rel == ".." || strings.HasPrefix(rel, prefix) {
		return false
	}
	return !filepath.IsAbs(rel)
}
