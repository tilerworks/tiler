package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jiangxin/tiler/internal/config"
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
		full      bool
		incr      bool
		imageName string
	)
	cmd := &cobra.Command{
		Use:   "build [dir]",
		Short: "Build a full or incremental image",
		Long: "Build packs dir, or the working directory when omitted, into a\n" +
			"new full image. Inside a merged mount view the default is an\n" +
			"incremental build that appends the writable upper as one layer;\n" +
			"--full packs the whole view instead.",
		Args: maxOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBuild(cmd, args, full, incr, imageName)
		},
	}
	cmd.Flags().BoolVar(&full, "full", false, "force a full build")
	cmd.Flags().BoolVar(&incr, "incr", false, "force an incremental build")
	cmd.Flags().StringVar(&imageName, "image", "", "explicit owner/repo image name")
	return cmd
}

func runBuild(cmd *cobra.Command, args []string, full, incr bool, imageName string) error {
	cfg, ok := loadedConfig(cmd)
	if !ok {
		return errors.New("build: configuration is not loaded")
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
	switch decision.Mode {
	case mountinfo.ModeFull:
		return runFullBuild(cmd, cfg, decision)
	case mountinfo.ModeIncr:
		return runIncrBuild(cmd, cfg, decision)
	default:
		return fmt.Errorf("build: unknown mode %q", decision.Mode)
	}
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

func runFullBuild(cmd *cobra.Command, cfg config.Config, decision mountinfo.Decision) error {
	res, err := image.BuildFull(image.FullRequest{
		Config:  cfg,
		Name:    decision.Name.String(),
		SrcDir:  decision.SrcDir,
		Builder: Version,
		Refs:    session.Refs{Dir: cfg.SessionsDir},
		Run:     packTool,
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "full %s %s\n", decision.Name, res.ID)
	return err
}

func runIncrBuild(cmd *cobra.Command, cfg config.Config, decision mountinfo.Decision) error {
	res, err := image.BuildIncr(image.IncrRequest{
		Config:     cfg,
		Name:       decision.Name.String(),
		Upper:      decision.Upper,
		Refs:       session.Refs{Dir: cfg.SessionsDir},
		Run:        packTool,
		MountSmoke: incrSmoke,
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "incr %s %s\n", decision.Name, res.ID)
	return err
}
