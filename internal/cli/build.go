package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

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
	// hdiutilTool runs hdiutil create and convert. Nil keeps the
	// platform default, which executes hdiutil from PATH only on
	// darwin. Tests install a recorder and do not exec hdiutil.
	hdiutilTool image.ToolRunner
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
		Long: "Build packs dir into a new full image. Omitting dir uses the\n" +
			"working directory. \".\" and the absolute path of that directory\n" +
			"select the same source. The source must stay outside every\n" +
			"reserved tree and must not cover the images_dir, sessions_dir,\n" +
			"or cache_dir paths from .tiler.yaml, or $HOME/.tiler (real\n" +
			"paths, including symlinks).\n" +
			"Inside a merged mount view the default is an incremental build\n" +
			"that appends the writable upper as one layer. When the recorded\n" +
			"incremental layers plus that upper already exceed half the base\n" +
			"image, the default switches to a full rebuild and a warning is\n" +
			"printed; --incremental keeps the layer. --full always packs the\n" +
			"whole view. An identical full or incremental payload is a no-op.\n" +
			"With --image naming a different library image, a path under the\n" +
			"view (often a subdirectory) packs as that image's full source.\n" +
			"A writable dmg view has no incremental layer. The build bakes\n" +
			"the shadow into a new full image for the default, --incremental,\n" +
			"and --full when --image matches the mounted image.\n" +
			"\n" +
			"--format and --compression select the container. An omitted\n" +
			"format uses the image_format key in .tiler.yaml, then the host\n" +
			"default (dmg on macOS), then the installed tools: both stacks\n" +
			"select squashfs, erofs alone selects erofs, and squashfs alone\n" +
			"selects squashfs. An omitted compression uses the\n" +
			"image_compression key in .tiler.yaml, or that format's default.\n" +
			"--format squashfs or erofs on macOS is a usage error.\n" +
			"An incremental build inherits the base image; a flag that\n" +
			"disagrees is a usage error.",
		Args: maxOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBuild(cmd, args, full, incr, imageName, split, format, compression)
		},
	}
	cmd.Flags().BoolVarP(&full, "full", "f", false, "force a full build")
	cmd.Flags().BoolVarP(&incr, "incremental", "i", false, "force an incremental build")
	cmd.Flags().StringVarP(&imageName, "image", "I", "",
		"explicit image name (1 to 5 path segments)")
	cmd.Flags().StringVar(&format, "format", "", "container format: dmg, squashfs, or erofs")
	cmd.Flags().StringVar(&compression, "compression", "", "container compression; default is the format default")
	cmd.Flags().StringVarP(&split, "split", "s", "",
		"block size for split builds (e.g. 512M, 2G); omit to use the split_size key in .tiler.yaml; 0 disables for this build")
	return cmd
}

func runBuild(cmd *cobra.Command, args []string, full, incr bool, imageName, split, format, compression string) error {
	cfg, ok := loadedConfig(cmd)
	if !ok {
		return errors.New("build: configuration is not loaded")
	}
	// A dmg build uses hdiutil. It is not a selectable stack, so a
	// Mac without squashfs or erofs tools can still publish.
	if !dmgBuildPlanned(cmd, cfg, format) {
		if err := gateAnyStack("build"); err != nil {
			return err
		}
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
	table, fromDisk, err := buildMountTable()
	if err != nil {
		return fmt.Errorf("build: %w", err)
	}
	if fromDisk {
		target = resolveBuildPath(target)
	}
	decision, err := mountinfo.Decide(target, table, mountinfo.Prefixes{
		// Combination dirs live under sessions/<name>/runtime/.
		RuntimeDir: cfg.SessionsDir,
		ImagesDir:  cfg.ImagesDir,
	}, mountinfo.Explicit{
		Full:  full,
		Incr:  incr,
		Image: imageName,
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
	// A writable dmg view bakes its shadow into a new full image
	// whether the caller asked for -i, --full, or the default.
	if shadow, ok := dmgViewShadow(table, decision); ok {
		return runDMGBake(cmd, cfg, decision, shadow, format, compression)
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
		if err := refuseReadOnlyDMGPublish(table, decision); err != nil {
			return err
		}
		typ, comp, err := selectContainer(cmd, cfg, format, compression)
		if err != nil {
			return err
		}
		if err := refuseDMGSplit(cmd, cfg, typ); err != nil {
			return err
		}
		if typ == image.FormatDMG {
			if !darwinHost() {
				// Linux keeps the registry error: dmg mounts only
				// on macOS. A darwin host publishes without Lookup,
				// because the format is not a selectable stack and
				// tests on Linux simulate the host default.
				if _, err := container.Lookup(typ); err != nil {
					return usageError{fmt.Errorf("build: %w", err)}
				}
			}
			return runDMGCreate(cmd, cfg, decision, comp)
		}
		// dmg is not registered on Linux, so a build of that type
		// stays an unknown-format error there.
		if _, err := container.Lookup(typ); err != nil {
			return usageError{fmt.Errorf("build: %w", err)}
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

// chooseFormat applies the flag, then image_format, then the host
// default, then toolchain inference. A dmg host default rejects
// squashfs and erofs flags. An empty host default leaves inference
// unchanged.
func chooseFormat(cmd *cobra.Command, cfg config.Config, flag string) (string, error) {
	if cmd.Flags().Changed("format") {
		if flag == "" {
			return "", usageError{errors.New("build: --format is empty")}
		}
		if err := refuseDarwinForeignFormat(flag); err != nil {
			return "", err
		}
		return flag, nil
	}
	if cfg.ImageFormat != "" {
		return cfg.ImageFormat, nil
	}
	if typ := hostDefaultType(); typ != "" {
		return typ, nil
	}
	typ, err := container.InferType(toolLookPath)
	if err != nil {
		return "", fmt.Errorf("build: %w; check the environment with tiler doctor", err)
	}
	return typ, nil
}

func chooseCompression(cmd *cobra.Command, cfg config.Config, format container.Format, flag string) (string, error) {
	comp := format.DefaultCompression()
	pinned := false
	switch {
	case cmd.Flags().Changed("compression"):
		if flag == "" {
			return "", usageError{errors.New("build: --compression is empty")}
		}
		comp = flag
		pinned = true
	case cfg.ImageCompression != "":
		comp = cfg.ImageCompression
		pinned = true
	}
	if !format.AllowsCompression(comp) {
		return "", usageError{fmt.Errorf("build: %s compression %q is not allowed", format.Type(), comp)}
	}
	if format.Type() != image.FormatErofs {
		return comp, nil
	}
	return fitErofsCompression(comp, pinned)
}

// fitErofsCompression keeps a pinned compression only when mkfs.erofs
// can pack it. An omitted compression uses the first supported of
// zstd, lz4, and gzip. A missing or unreadable help text leaves the
// choice unchanged.
func fitErofsCompression(comp string, pinned bool) (string, error) {
	help, err := erofsHelp()
	if err != nil {
		return comp, nil
	}
	listed := container.ErofsCompressors(help)
	if len(listed) == 0 {
		return comp, nil
	}
	if pinned {
		if container.ErofsToolSupports(comp, listed) {
			return comp, nil
		}
		return "", usageError{fmt.Errorf(
			"build: mkfs.erofs has no %q compressor (available: %s)",
			comp, strings.Join(listed, ", "))}
	}
	picked, err := container.PickErofsCompression(listed)
	if err != nil {
		return "", fmt.Errorf("build: %w", err)
	}
	return picked, nil
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
	manifest, err := image.LoadManifest(repo.MetaPath())
	if err != nil {
		return false, fmt.Errorf("build: %w", err)
	}
	root, err := manifest.Full()
	if err != nil {
		return false, fmt.Errorf("build: %w", err)
	}
	upper, err := treeBytes(decision.Upper)
	if err != nil {
		return false, fmt.Errorf("build: upper: %w", err)
	}
	layers := chainBytes(manifest)
	if !exceedsHalfBase(root.Size, layers, upper) {
		return false, nil
	}
	_, err = fmt.Fprintf(stderr,
		"warning: incremental layers and upper (%d bytes) exceed 50%% of the base image (%d bytes); building a full image (pass --incremental to append a layer anyway)\n",
		layers+upper, root.Size)
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

// chainBytes sums the recorded layer sizes of the head stack: the
// incremental nodes a default build appends to.
func chainBytes(manifest image.Manifest) int64 {
	stack, err := manifest.HeadStack()
	if err != nil {
		return 0
	}
	var n int64
	for _, node := range stack[1:] {
		n += node.Size
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
		return mapBuildPathError(err)
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "full %s %s\n", decision.Name, res.ID)
	return err
}

// mapBuildPathError turns a reserved-tree refusal or name placement
// conflict into a usage error so packing a library or session
// ancestor exits 2 like mount.
func mapBuildPathError(err error) error {
	if errors.Is(err, config.ErrReservedPath) ||
		errors.Is(err, image.ErrNameConflict) ||
		errors.Is(err, image.ErrInvalidName) {
		return usageError{err}
	}
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
		return mapBuildPathError(err)
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
	manifest, err := image.LoadManifest(repo.MetaPath())
	if err != nil {
		return image.Format{}, err
	}
	return manifest.Format, nil
}

func runDMGCreate(cmd *cobra.Command, cfg config.Config, decision mountinfo.Decision, compression string) error {
	res, err := image.BuildDMG(image.DMGRequest{
		Config:      cfg,
		Name:        decision.Name.String(),
		SrcDir:      decision.SrcDir,
		Compression: compression,
		Builder:     Version,
		Refs:        session.Refs{Dir: cfg.SessionsDir},
		Run:         hdiutilTool,
	})
	if err != nil {
		return mapBuildPathError(err)
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "full %s %s\n", decision.Name, res.ID)
	return err
}

// runDMGBake converts the mounted image with its shadow. -i and
// --full both take this path and warn that dmg has no incremental
// layer. The live attach is left on the old base: this does not
// detach or attach, and it does not rewrite the session record.
func runDMGBake(cmd *cobra.Command, cfg config.Config, decision mountinfo.Decision, shadow, format, compression string) error {
	if err := refuseBakeFormat(cmd, format); err != nil {
		return err
	}
	if err := refuseDMGSplit(cmd, cfg, image.FormatDMG); err != nil {
		return err
	}
	comp, err := bakeCompression(cmd, compression)
	if err != nil {
		return err
	}
	// The live shadow belongs to the session's full id. After the
	// first publish that id rests in _hold/, not at image.dmg.
	mounted, err := mountedFullID(cfg.SessionsDir, shadow)
	if err != nil {
		return fmt.Errorf("build: %w", err)
	}
	if _, err := fmt.Fprintln(cmd.ErrOrStderr(),
		"warning: dmg has no incremental layer; baking a full image"); err != nil {
		return err
	}
	res, err := image.BuildDMG(image.DMGRequest{
		Config:      cfg,
		Name:        decision.Name.String(),
		Shadow:      shadow,
		MountedID:   mounted,
		Compression: comp,
		Builder:     Version,
		Refs:        session.Refs{Dir: cfg.SessionsDir},
		Run:         hdiutilTool,
	})
	if err != nil {
		return mapBuildPathError(err)
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "full %s %s\n", decision.Name, res.ID)
	return err
}

// mountedFullID is the full image id of the live mount that owns
// shadow. The shadow path is
// sessions/<name>/runtime/m<seq>/upper.shadow. A shadow that is not
// that layout, or a session that does not pin an id, returns empty.
func mountedFullID(sessionsDir, shadow string) (string, error) {
	name, runtime, ok := shadowSession(sessionsDir, shadow)
	if !ok {
		return "", nil
	}
	rec, err := (session.Store{Dir: sessionsDir}).Load(name)
	if err != nil {
		if errors.Is(err, session.ErrNotFound) || errors.Is(err, session.ErrInvalidName) {
			return "", nil
		}
		return "", err
	}
	if rec.State != session.StateActive {
		return "", nil
	}
	for _, action := range rec.Actions {
		if action.Type != session.TypeMount || action.Status == session.StatusUnmounted {
			continue
		}
		if runtimeID(action) != runtime || action.FullID == "" {
			continue
		}
		return action.FullID, nil
	}
	return "", nil
}

func shadowSession(sessionsDir, shadow string) (name, runtime string, ok bool) {
	if sessionsDir == "" || shadow == "" {
		return "", "", false
	}
	rel, err := filepath.Rel(filepath.Clean(sessionsDir), filepath.Clean(shadow))
	if err != nil || !filepath.IsLocal(rel) {
		return "", "", false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 4 || parts[1] != "runtime" || parts[3] != "upper.shadow" {
		return "", "", false
	}
	return parts[0], parts[2], true
}

func runtimeID(action session.Action) string {
	if action.RuntimeID != "" {
		return action.RuntimeID
	}
	if action.Seq > 0 {
		return fmt.Sprintf("m%d", action.Seq)
	}
	return ""
}

func refuseBakeFormat(cmd *cobra.Command, format string) error {
	if !cmd.Flags().Changed("format") {
		return nil
	}
	if err := refuseDarwinForeignFormat(format); err != nil {
		return err
	}
	if format != image.FormatDMG {
		return usageError{fmt.Errorf("build: --format %s is not a dmg image", format)}
	}
	return nil
}

// bakeCompression is the convert format. An omitted flag follows the
// mounted image. A set flag must be ulfo, ulmo, or udro.
func bakeCompression(cmd *cobra.Command, flag string) (string, error) {
	if !cmd.Flags().Changed("compression") {
		return "", nil
	}
	if flag == "" {
		return "", usageError{errors.New("build: --compression is empty")}
	}
	if !container.DMG().AllowsCompression(flag) {
		return "", usageError{fmt.Errorf("build: dmg compression %q is not allowed", flag)}
	}
	return flag, nil
}
