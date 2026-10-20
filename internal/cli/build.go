package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/jiangxin/tiler/internal/config"
	"github.com/jiangxin/tiler/internal/container"
	"github.com/jiangxin/tiler/internal/image"
	"github.com/jiangxin/tiler/internal/mountinfo"
	"github.com/jiangxin/tiler/internal/session"
	"github.com/spf13/cobra"
)

// Seams the build command runs through. Tests replace them to inject
// a mount table, a fake packer, and a passing mount smoke gate; the
// zero values keep the production defaults.
var (
	// loadMountTable reads the host mount table.
	loadMountTable = mountinfo.Load
	// packTool runs mksquashfs. Nil keeps the image package default.
	packTool image.ToolRunner
	// incrSmoke is the incremental mount gate. Nil keeps the default.
	incrSmoke func(string) error
)

func newBuildCmd() *cobra.Command {
	var (
		full        bool
		incr        bool
		imageName   string
		split       string
		format      string
		compression string
	)
	cmd := &cobra.Command{
		Use:   "build [dir]",
		Short: "Build a full or incremental image",
		Long: "Build packs dir, or the working directory when omitted, into a\n" +
			"new full image. Inside a merged mount view the default is an\n" +
			"incremental build that appends the writable upper as one layer.\n" +
			"When the recorded incremental layers plus that upper already\n" +
			"exceed half the base image, the default switches to a full\n" +
			"rebuild and a warning is printed; --incremental keeps the layer.\n" +
			"--full always packs the whole view.\n" +
			"\n" +
			"--format and --compression select the container. An omitted\n" +
			"format uses image_format, then the installed tools: both stacks\n" +
			"select squashfs, erofs alone selects erofs, and squashfs alone\n" +
			"selects squashfs. An omitted compression uses image_compression\n" +
			"or that format's default. An incremental build inherits the\n" +
			"base image; a flag that disagrees is a usage error.",
		Args: maxOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBuild(cmd, args, full, incr, imageName, split, format, compression)
		},
	}
	cmd.Flags().BoolVarP(&full, "full", "f", false, "force a full build")
	cmd.Flags().BoolVarP(&incr, "incremental", "i", false, "force an incremental build")
	cmd.Flags().StringVarP(&imageName, "image", "I", "", "explicit owner/repo image name")
	cmd.Flags().StringVar(&format, "format", "", "container format: squashfs or erofs")
	cmd.Flags().StringVar(&compression, "compression", "", "container compression; default is the format default")
	cmd.Flags().StringVarP(&split, "split", "s", "",
		"block size for split builds (e.g. 512M, 2G); 0 disables for this build")
	return cmd
}

func runBuild(cmd *cobra.Command, args []string, full, incr bool, imageName, split, format, compression string) error {
	cfg, ok := loadedConfig(cmd)
	if !ok {
		return errors.New("build: configuration is not loaded")
	}
	if err := gateAnyStack("build"); err != nil {
		return err
	}
	if cmd.Flags().Changed("split") {
		size, err := config.ParseByteSize(split)
		if err != nil {
			return usageError{fmt.Errorf("split: %w", err)}
		}
		cfg.SplitSize = size
	}
	target, err := buildTarget(args)
	if err != nil {
		return err
	}
	table, err := loadMountTable()
	if err != nil {
		return fmt.Errorf("build: %w", err)
	}
	decision, err := mountinfo.Decide(target, table, mountinfo.Prefixes{
		// Combination dirs live under sessions/<name>/runtime/.
		RuntimeDir: cfg.SessionsDir,
		ImagesDir:  cfg.ImagesDir,
	}, mountinfo.Explicit{
		Full:   full,
		Incr:   incr,
		Image:  imageName,
		HasDir: len(args) == 1,
		// The table paths answer while the view is listed; the
		// session record answers when a killed daemon left the
		// entry anonymous or it vanished entirely.
		Identity: session.CombinedIdentity{
			Primary: mountinfo.PathIdentity{
				Table:     table,
				ImagesDir: cfg.ImagesDir,
			},
			Fallback: session.Identity{
				Store: session.Store{Dir: cfg.SessionsDir},
			},
		},
	})
	if err != nil {
		// Every decision refusal is a problem with how the command
		// was invoked: flags, name, or the chosen directory.
		return usageError{err}
	}
	// --incremental is an explicit choice and is never rewritten. The size
	// check only changes the default inside a writable view.
	if decision.Mode == mountinfo.ModeIncr && !incr {
		promoted, err := promoteOversizedIncr(cmd.ErrOrStderr(), cfg, &decision)
		if err != nil {
			return err
		}
		if promoted {
			decision.Mode = mountinfo.ModeFull
			decision.SrcDir = decision.MountPoint
		}
	}
	switch decision.Mode {
	case mountinfo.ModeFull:
		typ, comp, err := resolveFullContainer(cmd, cfg, format, compression)
		if err != nil {
			return err
		}
		if err := gateFormatTools("build", typ); err != nil {
			return err
		}
		return runFullBuild(cmd, cfg, decision, typ, comp)
	case mountinfo.ModeIncr:
		return runIncrBuild(cmd, cfg, decision, format, compression)
	default:
		return fmt.Errorf("build: unknown mode %q", decision.Mode)
	}
}

// resolveFullContainer applies flag, then config, then toolchain
// inference. An illegal pair, including erofs with xz, is a usage error.
func resolveFullContainer(cmd *cobra.Command, cfg config.Config, format, compression string) (string, string, error) {
	typ, err := chooseFormat(cmd, cfg, format)
	if err != nil {
		return "", "", err
	}
	parsed, err := container.Lookup(typ)
	if err != nil {
		return "", "", usageError{fmt.Errorf("build: %w", err)}
	}
	comp, err := chooseCompression(cmd, cfg, parsed, compression)
	if err != nil {
		return "", "", err
	}
	return parsed.Type(), comp, nil
}

func chooseFormat(cmd *cobra.Command, cfg config.Config, flag string) (string, error) {
	if cmd.Flags().Changed("format") {
		if flag == "" {
			return "", usageError{errors.New("build: --format is empty")}
		}
		return flag, nil
	}
	if cfg.ImageFormat != "" {
		return cfg.ImageFormat, nil
	}
	typ, err := container.InferType(toolLookPath)
	if err != nil {
		return "", fmt.Errorf("build: %w; check the environment with tiler doctor", err)
	}
	return typ, nil
}

func chooseCompression(cmd *cobra.Command, cfg config.Config, format container.Format, flag string) (string, error) {
	comp := format.DefaultCompression()
	switch {
	case cmd.Flags().Changed("compression"):
		if flag == "" {
			return "", usageError{errors.New("build: --compression is empty")}
		}
		comp = flag
	case cfg.ImageCompression != "":
		comp = cfg.ImageCompression
	}
	if !format.AllowsCompression(comp) {
		return "", usageError{fmt.Errorf("build: %s compression %q is not allowed", format.Type(), comp)}
	}
	return comp, nil
}

func explicitContainer(cmd *cobra.Command, format, compression string) (string, string, error) {
	var typ, comp string
	if cmd.Flags().Changed("format") {
		if format == "" {
			return "", "", usageError{errors.New("build: --format is empty")}
		}
		typ = format
	}
	if cmd.Flags().Changed("compression") {
		if compression == "" {
			return "", "", usageError{errors.New("build: --compression is empty")}
		}
		comp = compression
	}
	return typ, comp, nil
}

// buildTarget is the cleaned absolute path the decision runs on: the
// source argument when given, the working directory otherwise.
func buildTarget(args []string) (string, error) {
	if len(args) == 1 {
		abs, err := filepath.Abs(args[0])
		if err != nil {
			return "", fmt.Errorf("build: source: %w", err)
		}
		return abs, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("build: working directory: %w", err)
	}
	return cwd, nil
}

// promoteOversizedIncr reports whether an incremental default should
// become a full rebuild. The recorded chain plus the live upper must
// strictly exceed half the base image size. A missing or zero base
// size leaves the incremental default alone.
func promoteOversizedIncr(stderr io.Writer, cfg config.Config, decision *mountinfo.Decision) (bool, error) {
	if decision.MountPoint == "" || decision.Upper == "" {
		return false, nil
	}
	repo, err := image.OpenRepo(cfg.ImagesDir, decision.Name.String())
	if err != nil {
		return false, fmt.Errorf("build: %w", err)
	}
	meta, err := image.Load(repo.MetaPath())
	if err != nil {
		return false, fmt.Errorf("build: %w", err)
	}
	upper, err := treeBytes(decision.Upper)
	if err != nil {
		return false, fmt.Errorf("build: upper: %w", err)
	}
	layers := chainBytes(meta)
	if !exceedsHalfBase(meta.Size, layers, upper) {
		return false, nil
	}
	_, err = fmt.Fprintf(stderr,
		"warning: incremental layers and upper (%d bytes) exceed 50%% of the base image (%d bytes); building a full image (pass --incremental to append a layer anyway)\n",
		layers+upper, meta.Size)
	return err == nil, err
}

// exceedsHalfBase is true when layers+upper is strictly more than
// half of base. A non-positive base never trips the rule.
func exceedsHalfBase(base, layers, upper int64) bool {
	if base <= 0 {
		return false
	}
	sum := layers + upper
	if sum < layers {
		return true
	}
	return sum > base/2
}

func chainBytes(meta image.Meta) int64 {
	var n int64
	for _, entry := range meta.Chain {
		n += entry.Size
	}
	return n
}

// treeBytes sums non-directory sizes under root. Symlinks contribute
// their own length, not the target.
func treeBytes(root string) (int64, error) {
	var n int64
	err := filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		n += info.Size()
		return nil
	})
	return n, err
}

func runFullBuild(cmd *cobra.Command, cfg config.Config, decision mountinfo.Decision, format, compression string) error {
	res, err := image.BuildFull(image.FullRequest{
		Config:      cfg,
		Name:        decision.Name.String(),
		SrcDir:      decision.SrcDir,
		Builder:     Version,
		Format:      format,
		Compression: compression,
		Refs:        session.Refs{Dir: cfg.SessionsDir},
		Run:         packTool,
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "full %s %s\n", decision.Name, res.ID)
	return err
}

func runIncrBuild(cmd *cobra.Command, cfg config.Config, decision mountinfo.Decision, format, compression string) error {
	typ, comp, err := explicitContainer(cmd, format, compression)
	if err != nil {
		return err
	}
	if active, readErr := readActiveFormat(cfg, decision.Name.String()); readErr == nil {
		if err := image.FormatConflict(active, typ, comp); err != nil {
			return usageError{err}
		}
		if err := gateFormatTools("build", active.Type); err != nil {
			return err
		}
	}
	res, err := image.BuildIncr(image.IncrRequest{
		Config:      cfg,
		Name:        decision.Name.String(),
		Format:      typ,
		Compression: comp,
		Upper:       decision.Upper,
		Refs:        session.Refs{Dir: cfg.SessionsDir},
		Run:         packTool,
		MountSmoke:  incrSmoke,
	})
	if errors.Is(err, image.ErrFormatConflict) {
		return usageError{err}
	}
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "incr %s %s\n", decision.Name, res.ID)
	return err
}

// readActiveFormat loads the container recorded on the active image.
// A missing image is left for BuildIncr to report.
func readActiveFormat(cfg config.Config, name string) (image.Format, error) {
	repo, err := image.OpenRepo(cfg.ImagesDir, name)
	if err != nil {
		return image.Format{}, err
	}
	meta, err := image.Load(repo.MetaPath())
	if err != nil {
		return image.Format{}, err
	}
	return meta.Format, nil
}
