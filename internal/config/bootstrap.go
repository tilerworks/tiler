package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// userFileTemplate is written to $HOME/.tiler.yaml the first time
// tiler runs and the file is missing. Every value matches the
// built-in defaults, so creating the file does not change behavior;
// it only gives the user a documented, editable starting point.
// Directory values are relative to this file. A ~/ prefix expands
// with the process home (see resolvePath) and points at the wrong
// tree when that home is not the directory holding the file, such
// as an e2e run whose HOME differs from the config location.
const userFileTemplate = `# tiler user configuration (auto-generated on first run).
# Edit or comment out keys to override built-in defaults.
# Relative paths resolve against this file's directory; ~/ expands to $HOME.
version: 1
images_dir: .tiler/images
sessions_dir: .tiler/sessions      # merged root: records + per-session runtime
cache_dir: .tiler/cache
logs_dir: .tiler/logs
log_level: info
# image_format: squashfs        # squashfs or erofs; omit to infer from tools
# image_compression: zstd       # omit to use the format default
# max_layers: 32                # full image plus increments; excludes writable upper
# split_size: 2G                # 0 disables splitting; bare bytes or k/m/g/t
`

// EnsureUserFile creates $HOME/.tiler.yaml from userFileTemplate when
// it does not already exist. An existing file is never read, rewritten,
// or reformatted. The write is atomic: the template lands in a temporary
// file in the same directory, then renames over the target, so a reader
// never observes a partial file. A concurrent creator that wins the race
// is not an error — the target already existing means the goal is met.
//
// EnsureUserFile is best-effort at the call site: a failure to write
// (for example a read-only home) must not stop tiler, because Load
// resolves the same defaults without the file.
func EnsureUserFile(home string) error {
	path := filepath.Join(home, fileName)
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	tmp, err := os.CreateTemp(home, fileName+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// Any failure past this point must not leave the temp file behind.
	defer func() {
		if tmpName != "" {
			_ = os.Remove(tmpName)
		}
	}()

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(userFileTemplate); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		// A concurrent run may have created the target first; that is
		// the desired end state, so treat an existing file as success.
		if _, statErr := os.Stat(path); statErr == nil {
			tmpName = ""
			return nil
		}
		return err
	}
	tmpName = ""
	return nil
}
