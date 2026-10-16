package cli

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/jiangxin/tiler/internal/config"
	"github.com/jiangxin/tiler/internal/image"
	"github.com/jiangxin/tiler/internal/mountinfo"
	"github.com/jiangxin/tiler/internal/workspace"
	"github.com/spf13/cobra"
)

// Seams the mount and umount commands run through. Tests replace
// them to inject a fake tool runner, a fake cache, a fake mount
// table, and fake engines; the zero values keep the production
// defaults. The engine seams exist because the engine confirms each
// mount with a live stat probe, which a fake tool runner can never
// satisfy without real FUSE mounts.
var (
	// mountRun runs the FUSE tools inside the engine. Nil keeps the
	// engine default.
	mountRun func(name string, args ...string) error
	// ensureCache copies one image into the cache directory.
	ensureCache = image.EnsureCached
	// mountEngine builds the workspace mount.
	mountEngine = workspace.Mount
	// unmountEngine tears a view down by path.
	unmountEngine = workspace.UnmountPath
)

// mountFlags carries the local flags of one mount invocation.
type mountFlags struct {
	readOnly bool
	subdir   string
	verify   bool
}

func newMountCmd() *cobra.Command {
	var flags mountFlags
	cmd := &cobra.Command{
		Use:   "mount <image> <dir>",
		Short: "Mount an image as a workspace",
		Long: "Mount serves <image> at <dir>: a writable merged view by\n" +
			"default, or a read-only view with --read-only. <image> is an\n" +
			"owner/repo library reference or the path of a single squashfs\n" +
			"file; the library reference wins when an argument is both.\n" +
			"Chain layers mount as a cascade, --subdir exposes one image\n" +
			"subdirectory as the mount root, and --verify re-hashes every\n" +
			"recorded digest instead of the default quick check.",
		Args: exactTwoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMount(cmd, args, flags)
		},
	}
	cmd.Flags().BoolVar(&flags.readOnly, "read-only", false, "mount without a writable upper")
	cmd.Flags().StringVar(&flags.subdir, "subdir", "", "image subdirectory exposed as the mount root")
	cmd.Flags().BoolVar(&flags.verify, "verify", false, "re-verify every recorded digest with sha256")
	return cmd
}

func runMount(cmd *cobra.Command, args []string, flags mountFlags) error {
	cfg, ok := loadedConfig(cmd)
	if !ok {
		return errors.New("mount: configuration is not loaded")
	}
	dir, err := filepath.Abs(args[1])
	if err != nil {
		return fmt.Errorf("mount: target: %w", err)
	}
	if err := config.OutsideReserved(dir, cfg); err != nil {
		return usageError{err}
	}
	if err := workspace.CheckSubdir(flags.subdir); err != nil {
		return usageError{err}
	}
	target, err := image.ResolveMount(cfg, args[0])
	if err != nil {
		if isResolveUsage(err) {
			return usageError{err}
		}
		return err
	}
	if flags.verify && !target.Library {
		return usageError{errors.New(
			"mount: --verify needs an owner/repo image; a file path records no digest")}
	}
	if err := verifyMembers(target, flags.verify); err != nil {
		return err
	}
	req, err := mountRequest(cfg, target, dir, flags)
	if err != nil {
		return err
	}
	ws, err := mountEngine(req)
	if err != nil {
		if errors.Is(err, workspace.ErrUnavailable) {
			return fmt.Errorf("%w; check the environment with tiler doctor", err)
		}
		return err
	}
	verb := "mounted"
	if ws.Reused {
		verb = "already mounted"
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", verb, dir)
	return err
}

// isResolveUsage reports whether a resolve failure rejects the
// argument shape itself, which is a command-line usage problem. A
// library state failure stays a runtime error.
func isResolveUsage(err error) bool {
	return errors.Is(err, image.ErrUnknownImage) ||
		errors.Is(err, image.ErrNotImageFile) ||
		errors.Is(err, image.ErrInvalidName) ||
		errors.Is(err, image.ErrInvalidPath)
}

// verifyMembers checks every library member against its recorded
// facts: the cheap size and mtime probe by default, the full sha256
// re-hash with --verify. A file-path target records no facts and
// skips verification.
func verifyMembers(target image.Target, verify bool) error {
	if !target.Library {
		return nil
	}
	for _, m := range target.Members {
		meta := image.Meta{Size: m.Size, MtimeUnix: m.MtimeUnix, SHA256: m.SHA256}
		var err error
		if verify {
			err = image.VerifySHA256(meta, m.Path)
		} else {
			err = image.QuickCheck(meta, m.Path)
		}
		if err != nil {
			return fmt.Errorf("mount: %w", err)
		}
	}
	return nil
}

// mountRequest assembles the engine request. With the cache enabled
// every member mounts from its verified local copy while fsname
// keeps the original path, so the mount table identity survives the
// copy.
func mountRequest(
	cfg config.Config,
	target image.Target,
	dir string,
	flags mountFlags,
) (workspace.Request, error) {
	mounted := make([]string, 0, len(target.Members))
	original := make([]string, 0, len(target.Members))
	for _, m := range target.Members {
		path := m.Path
		if cfg.CacheEnabled {
			cached, _, err := ensureCache(cfg.CacheDir, m.Path, m.SHA256)
			if err != nil {
				return workspace.Request{}, fmt.Errorf("mount: %w", err)
			}
			path = cached
		}
		mounted = append(mounted, path)
		original = append(original, m.Path)
	}
	table, err := loadMountTable()
	if err != nil {
		return workspace.Request{}, fmt.Errorf("mount: %w", err)
	}
	req := workspace.Request{
		Full:            mounted[0],
		Layers:          mounted[1:],
		ReadOnly:        flags.readOnly,
		Subdir:          flags.subdir,
		Table:           toEntries(table),
		SquashfsOptions: cfg.SquashfsOptions,
		FuseOptions:     cfg.FuseOptions,
		RuntimeDir:      cfg.RuntimeDir,
		MergedDir:       dir,
		Run:             mountRun,
	}
	if cfg.CacheEnabled {
		req.FullFSName = original[0]
		req.LayerFSNames = original[1:]
	}
	return req, nil
}

// toEntries converts mount table rows into the minimal entries the
// engine consumes, keeping the engine free of a mountinfo import.
func toEntries(table []mountinfo.Mount) []workspace.Entry {
	entries := make([]workspace.Entry, 0, len(table))
	for _, m := range table {
		entries = append(entries, workspace.Entry{
			Point:  m.Point,
			FSType: m.FSType,
			Source: m.Source,
			Upper:  m.Upper,
			Lowers: m.Lowers,
		})
	}
	return entries
}
