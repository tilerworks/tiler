package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/jiangxin/tiler/internal/config"
	"github.com/jiangxin/tiler/internal/container"
	"github.com/jiangxin/tiler/internal/image"
	"github.com/jiangxin/tiler/internal/mountinfo"
	"github.com/spf13/cobra"
)

// hostDefaultType is the container used when build has neither
// --format nor image_format. Darwin returns dmg. Every other host
// returns an empty string and InferType still runs. Tests replace
// it so the darwin policy runs on Linux.
var hostDefaultType = nativeHostDefaultType

func nativeHostDefaultType() string {
	if runtime.GOOS == "darwin" {
		return image.FormatDMG
	}
	return ""
}

func darwinHost() bool {
	return hostDefaultType() == image.FormatDMG
}

func refuseDarwinForeignFormat(flag string) error {
	if !darwinHost() {
		return nil
	}
	switch flag {
	case image.FormatSquashfs, image.FormatErofs:
		return usageError{fmt.Errorf(
			"build: --format %s is not available on macOS; use dmg", flag)}
	default:
		return nil
	}
}

// selectContainer applies the flag, then image_format, then the host
// default, then toolchain inference. dmg compression is judged from
// the format value, not the registry, so a Linux test can reject
// zstd. The caller still requires Lookup before packing.
func selectContainer(cmd *cobra.Command, cfg config.Config, format, compression string) (string, string, error) {
	typ, err := chooseFormat(cmd, cfg, format)
	if err != nil {
		return "", "", err
	}
	parsed, err := formatForSelection(typ)
	if err != nil {
		return "", "", usageError{fmt.Errorf("build: %w", err)}
	}
	comp, err := chooseCompression(cmd, cfg, parsed, compression)
	if err != nil {
		return "", "", err
	}
	return parsed.Type(), comp, nil
}

// formatForSelection returns the format that judges compression.
// dmg is returned even when it is not registered.
func formatForSelection(typ string) (container.Format, error) {
	if typ == image.FormatDMG {
		return container.DMG(), nil
	}
	return container.Lookup(typ)
}

func refuseDMGSplit(cmd *cobra.Command, cfg config.Config, typ string) error {
	if typ != image.FormatDMG {
		return nil
	}
	if cmd.Flags().Changed("split") || cfg.SplitSize > 0 {
		return usageError{errors.New("build: --split is not available for dmg")}
	}
	return nil
}

func refuseDMGSubdir(subdir, format string) error {
	if subdir == "" || format != image.FormatDMG {
		return nil
	}
	return usageError{errors.New("mount: --subdir is not available for dmg")}
}

// refuseReadOnlyDMGPublish rejects baking a read-only view whose
// mounted source is a dmg. The source is the file behind the view,
// not the repository named by --image: an image.dmg path or a koly
// trailer is a usage error even when that name is missing or is a
// different repository. A read error on the mounted file is
// returned; it is not treated as "not dmg". A writable view has an
// upper and is not this refusal.
func refuseReadOnlyDMGPublish(table []mountinfo.Mount, decision mountinfo.Decision) error {
	if decision.MountPoint == "" || !readOnlyView(table, decision.MountPoint) {
		return nil
	}
	sources := mountedSources(table, decision.MountPoint)
	for _, source := range sources {
		if filepath.Base(source) == image.ImageDMG {
			return readOnlyDMGUsage()
		}
	}
	for _, source := range sources {
		dmg, err := regularFileKoly(source)
		if err != nil {
			return fmt.Errorf("build: read mounted source %s: %w", source, err)
		}
		if dmg {
			return readOnlyDMGUsage()
		}
	}
	return nil
}

func readOnlyDMGUsage() error {
	return usageError{errors.New("build: a read-only dmg view cannot be published")}
}

// mountedSources is the files a view is serving. A merged view's
// lower mount points are replaced by those mounts' sources, which
// is where image.dmg and image.sqsh appear.
func mountedSources(table []mountinfo.Mount, point string) []string {
	view, ok := mountByPoint(table, point)
	if !ok {
		return nil
	}
	candidates := make([]string, 0, 1+len(view.Lowers))
	if view.Source != "" {
		candidates = append(candidates, view.Source)
	}
	candidates = append(candidates, view.Lowers...)
	sources := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		source := candidate
		if mount, ok := mountByPoint(table, candidate); ok && mount.Source != "" {
			source = mount.Source
		}
		if source != "" {
			sources = append(sources, source)
		}
	}
	return sources
}

func mountByPoint(table []mountinfo.Mount, point string) (mountinfo.Mount, bool) {
	found := mountinfo.Mount{}
	ok := false
	for _, mount := range table {
		if mount.Point == point {
			found, ok = mount, true
		}
	}
	return found, ok
}

// regularFileKoly reports the UDIF trailer of a regular file. A
// missing path is a mount pseudo-source such as "overlay", not a
// dmg. Any other stat or read error is returned.
func regularFileKoly(source string) (bool, error) {
	if source == "" {
		return false, nil
	}
	info, err := os.Stat(source)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, nil
	}
	return container.ReadKolyTrailer(source)
}

func readOnlyView(table []mountinfo.Mount, point string) bool {
	found := false
	upper := ""
	for _, mount := range table {
		if mount.Point != point {
			continue
		}
		found = true
		upper = mount.Upper
	}
	return found && upper == ""
}
