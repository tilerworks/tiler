//go:build !linux

package workspace

import (
	"errors"
	"os"
)

// errPruneUnsupported is what every platform probe reports off
// Linux. The overlay markers live in the trusted xattr namespace
// and the raw stat fields differ per platform, so the prune rules
// degrade to always-conservative keeps instead of guessing: with
// these stubs PruneUpper removes nothing, and the root marker probe
// fails first, keeping the whole tree with a single counted error.
var errPruneUnsupported = errors.New("workspace: upper pruning needs linux overlay metadata")

// platformStatOf always fails off Linux; entries are kept fail-safe.
func platformStatOf(os.FileInfo) (platformStat, error) {
	return platformStat{}, errPruneUnsupported
}

// overlayXattrs always fails off Linux; entries are kept fail-safe.
func overlayXattrs(string) (map[string][]byte, error) {
	return nil, errPruneUnsupported
}

// stripOverlayXattr always fails off Linux; markers are never
// touched.
func stripOverlayXattr(string, string) error {
	return errPruneUnsupported
}
