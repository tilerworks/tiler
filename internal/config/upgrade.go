package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const (
	backupSuffix = ".bak"
	// removedRuntimeDir is the former global runtime root key. Its
	// presence alone marks a user file as incompatible with the
	// current schema, even when version is already current.
	removedRuntimeDir = "runtime_dir"
)

// UpgradeUserFile rewrites $HOME/.tiler.yaml when it lacks a current
// schema version or still carries removed keys such as runtime_dir.
// A missing file is a no-op — EnsureUserFile owns first-create with a
// current template. Compatible files are left untouched.
//
// The rewrite is minimal: keys are re-serialized from a loose parse, so
// comments and key order may be lost. A .tiler.yaml.bak backup of the
// original bytes is written first (atomic temp + rename, mode 0600),
// then the upgraded document replaces the user file the same way.
//
// UpgradeUserFile must run before Load's strict Decode so a leftover
// runtime_dir never surfaces as an unknown-field parse error.
func UpgradeUserFile(home string) error {
	path := filepath.Join(home, fileName)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}

	raw, err := decodeLooseUserFile(data)
	if err != nil {
		return fmt.Errorf("upgrade %s: %w", path, err)
	}
	version, err := looseVersion(raw)
	if err != nil {
		return fmt.Errorf("upgrade %s: %w", path, err)
	}
	_, hasRemoved := raw[removedRuntimeDir]
	if version >= CurrentConfigVersion && !hasRemoved {
		return nil
	}

	delete(raw, removedRuntimeDir)
	raw["version"] = CurrentConfigVersion

	upgraded, err := yaml.Marshal(raw)
	if err != nil {
		return fmt.Errorf("upgrade %s: marshal: %w", path, err)
	}
	if err := writeUserBackup(path, data); err != nil {
		return fmt.Errorf("upgrade %s: backup: %w", path, err)
	}
	if err := writeUserFileAtomic(path, upgraded); err != nil {
		return fmt.Errorf("upgrade %s: write: %w", path, err)
	}
	return nil
}

// decodeLooseUserFile parses the user file without KnownFields so
// removed keys remain visible for the upgrade decision.
func decodeLooseUserFile(data []byte) (map[string]any, error) {
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	if raw == nil {
		raw = map[string]any{}
	}
	return raw, nil
}

func looseVersion(raw map[string]any) (int, error) {
	v, ok := raw["version"]
	if !ok || v == nil {
		return 0, nil
	}
	switch n := v.(type) {
	case int:
		return n, nil
	case int64:
		return int(n), nil
	case uint64:
		return int(n), nil
	case float64:
		// yaml.v3 decodes float scalars (including whole-number
		// 1.0) as float64 into map[string]any. Decode rejects those
		// via strconv.ParseInt on the scalar text, so treating 1.0
		// as already-current would leave Load permanently failing.
		// Fractional values are invalid; whole-number floats are
		// reported as outdated (0) so UpgradeUserFile rewrites them
		// to an integer CurrentConfigVersion scalar Decode accepts.
		if n != float64(int(n)) {
			return 0, fmt.Errorf("version: invalid integer")
		}
		return 0, nil
	default:
		return 0, fmt.Errorf("version: invalid integer")
	}
}

func writeUserBackup(path string, data []byte) error {
	backup := path + backupSuffix
	return writeUserFileAtomic(backup, data)
}

func writeUserFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if tmpName != "" {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	tmpName = ""
	return nil
}
