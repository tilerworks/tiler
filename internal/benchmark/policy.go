package benchmark

import (
	"fmt"
	"strings"

	"github.com/jiangxin/tiler/internal/config"
	"github.com/jiangxin/tiler/internal/container"
)

type actions struct {
	Create bool
	Read   bool
	Probe  bool
}

func (a actions) usesBench() bool { return a.Create || a.Probe }

func parseMode(mode string) (string, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		return "both", nil
	}
	switch mode {
	case "write", "read", "both":
		return mode, nil
	default:
		return "", usagef("benchmark: --mode %q must be write, read, or both", mode)
	}
}

func parseSizes(size, fileSize string) (int64, int64, error) {
	n, err := parseOneSize("--size", size)
	if err != nil {
		return 0, 0, err
	}
	f, err := parseOneSize("--file-size", fileSize)
	if err != nil {
		return 0, 0, err
	}
	return n, f, nil
}

func parseOneSize(flag, raw string) (int64, error) {
	n, err := config.ParseByteSize(raw)
	if err != nil {
		return 0, usagef("benchmark: %s: %s", flag, err.Error())
	}
	if n < 1 {
		return 0, usagef("benchmark: %s must be at least 1 byte", flag)
	}
	return n, nil
}

func parseType(typ, compress, goos string) (string, string, error) {
	typ = strings.ToLower(strings.TrimSpace(typ))
	if typ == "" {
		if goos != "darwin" {
			return "", "", usagef("benchmark: --type is required")
		}
		typ = "dmg"
	}
	if err := typeOnHost(typ, goos); err != nil {
		return "", "", err
	}
	chosen, err := canonicalCompress(typ, compress)
	if err != nil {
		return "", "", err
	}
	return typ, chosen, nil
}

func typeOnHost(typ, goos string) error {
	switch typ {
	case "dmg":
		if goos != "darwin" {
			return usagef("benchmark: --type dmg is only available on darwin")
		}
	case "squashfs", "erofs":
		if goos != "linux" {
			return usagef("benchmark: --type %s is only available on linux", typ)
		}
	case "tar":
		return nil
	default:
		return usagef("benchmark: --type %q must be dmg, tar, squashfs, or erofs", typ)
	}
	return nil
}

func canonicalCompress(typ, compress string) (string, error) {
	raw := strings.TrimSpace(compress)
	lower := strings.ToLower(raw)
	switch typ {
	case "tar":
		if raw != "" {
			return "", usagef("benchmark: --type tar does not accept --compress")
		}
		return "", nil
	case "dmg":
		return dmgCompress(raw, lower)
	case "squashfs", "erofs":
		return formatCompress(typ, raw, lower)
	default:
		return "", usagef("benchmark: --type %q must be dmg, tar, squashfs, or erofs", typ)
	}
}

func dmgCompress(raw, lower string) (string, error) {
	if !container.DMG().AllowsCompression(lower) {
		return "", usagef("benchmark: dmg --compress %q must be ULFO, ULMO, or UDRO", raw)
	}
	if lower == "" {
		lower = container.DMG().DefaultCompression()
	}
	return lower, nil
}

func formatCompress(typ, raw, lower string) (string, error) {
	format, err := container.Lookup(typ)
	if err != nil {
		return "", fmt.Errorf("benchmark: %w", err)
	}
	if lower == "" {
		if typ == "erofs" {
			return "", nil
		}
		return format.DefaultCompression(), nil
	}
	if !format.AllowsCompression(lower) {
		return "", usagef("benchmark: %s --compress %q is not allowed", typ, raw)
	}
	return lower, nil
}

func decide(mode string, opts Options) (actions, error) {
	if opts.ReadOnly {
		return decideReadonly(mode, opts)
	}
	image := strings.TrimSpace(opts.Image) != ""
	if mode == "read" && image && !opts.NoProbe {
		return actions{}, usagef("benchmark: --mode read --image requires --no-probe")
	}
	create := mode != "read" || !image
	if image && create {
		return actions{}, usagef("benchmark: --image cannot be combined with creating an image")
	}
	return actions{
		Create: create,
		Read:   mode != "write",
		Probe:  !opts.NoProbe,
	}, nil
}

func decideReadonly(mode string, opts Options) (actions, error) {
	if strings.TrimSpace(opts.Image) == "" {
		return actions{}, usagef("benchmark: --readonly requires --image")
	}
	if !opts.NoProbe {
		return actions{}, usagef("benchmark: --readonly requires --no-probe")
	}
	if strings.TrimSpace(opts.BenchDir) != "" {
		return actions{}, usagef("benchmark: --readonly cannot be combined with --bench-dir")
	}
	if mode != "read" {
		return actions{}, usagef("benchmark: --readonly requires --mode read")
	}
	return actions{Read: true}, nil
}

func probeOps(n int) (int, error) {
	if n == 0 {
		return 2000, nil
	}
	if n < 1 {
		return 0, usagef("benchmark: --probe-ops must be at least 1")
	}
	return n, nil
}
