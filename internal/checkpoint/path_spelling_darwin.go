//go:build darwin

package checkpoint

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jiangxin/tiler/internal/config"
	"golang.org/x/sys/unix"
)

type darwinPolicyPath struct {
	parts      []string
	existing   int
	foldSuffix bool
}

func (p darwinPolicyPath) within(root darwinPolicyPath) bool {
	if len(root.parts) > len(p.parts) {
		return false
	}
	for i, name := range root.parts {
		part := p.parts[i]
		if part == name {
			continue
		}
		if i < p.existing && i < root.existing {
			return false
		}
		fold := (i >= p.existing && p.foldSuffix) || (i >= root.existing && root.foldSuffix)
		if !darwinNamesMayAlias(part, name, fold) {
			return false
		}
	}
	return true
}

// These are the non-ASCII characters with all-ASCII canonical
// decompositions, plus the all-ASCII expansions used by Unicode case folding.
var darwinASCIIDecompositions = strings.NewReplacer(";", ";", "`", "`", "K", "K")
var darwinASCIICaseFolds = strings.NewReplacer(
	"ß", "ss", "ẞ", "ss", "ſ", "s", "ﬀ", "ff", "ﬁ", "fi", "ﬂ", "fl",
	"ﬃ", "ffi", "ﬄ", "ffl", "ﬅ", "st", "ﬆ", "st",
)

func darwinNamesMayAlias(a, b string, fold bool) bool {
	a, b = darwinASCIIDecompositions.Replace(a), darwinASCIIDecompositions.Replace(b)
	if fold {
		a = strings.ToLower(darwinASCIICaseFolds.Replace(a))
		b = strings.ToLower(darwinASCIICaseFolds.Replace(b))
	}
	if a == b {
		return true
	}
	ai := strings.IndexFunc(a, func(r rune) bool { return r >= 128 })
	bi := strings.IndexFunc(b, func(r rune) bool { return r >= 128 })
	if ai < 0 || bi < 0 {
		return false
	}
	// The filesystem can normalize missing Unicode names. If their ASCII
	// prefixes cannot distinguish them, keep the protected path untouched.
	end := min(ai, bi)
	return a[:end] == b[:end]
}

func checkPlatformProtectedPaths(path, home string, cfg config.Config) error {
	target, err := darwinComparePath(path)
	if err != nil {
		return err
	}
	if home != "" {
		protected, err := darwinComparePath(home)
		if err != nil {
			return err
		}
		if protected.within(target) {
			return fmt.Errorf("protected path: %s", path)
		}
	}
	for _, root := range cfg.ReservedPaths() {
		protected, err := darwinComparePath(root)
		if err != nil {
			return err
		}
		if target.within(protected) || protected.within(target) {
			return fmt.Errorf("%w: %s intersects %s", config.ErrReservedPath, path, root)
		}
	}
	return nil
}

func darwinComparePath(path string) (darwinPolicyPath, error) {
	var result darwinPolicyPath
	path, err := filepath.Abs(path)
	if err != nil {
		return result, err
	}
	var missing string
	for {
		physical, err := filepath.EvalSymlinks(path)
		if errors.Is(err, os.ErrNotExist) {
			missing = filepath.Join(filepath.Base(path), missing)
			path = filepath.Dir(path)
			continue
		}
		if err != nil {
			return result, err
		}
		dir, err := openDirectory(physical)
		if err != nil {
			return result, err
		}
		defer func() { _ = dir.Close() }()
		physical, err = directoryPath(dir)
		if err != nil {
			return result, err
		}
		result.parts = darwinPathComponents(physical)
		result.existing = len(result.parts)
		if missing == "" {
			return result, nil
		}
		result.parts = append(result.parts, darwinPathComponents(missing)...)
		// Darwin's sys/unistd.h defines _PC_CASE_SENSITIVE as 11.
		const pathconfCaseSensitive = 11
		sensitive, err := unix.Fpathconf(int(dir.Fd()), pathconfCaseSensitive)
		if err != nil {
			return result, err
		}
		result.foldSuffix = sensitive != 1
		return result, nil
	}
}

func darwinPathComponents(path string) []string {
	return strings.FieldsFunc(path, func(r rune) bool { return r == '/' })
}
