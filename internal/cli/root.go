// Package cli is the only cobra command tree. Startup loads
// configuration and rejects a working directory inside a reserved
// tree. Usage failures map to exit code 2.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/jiangxin/tiler/internal/config"
	"github.com/spf13/cobra"
)

// Version is printed by the version command. It stays dev when the
// build does not pass -X github.com/jiangxin/tiler/internal/cli.Version.
var Version = "dev"

// filesystemRoot is the walk limit for project files outside the
// user home. Startup does not evaluate symlinks.
const filesystemRoot = "/"

type loadedConfigKey struct{}

// NewRootCmd assembles the tiler command tree. Persistent flags
// overlay configuration only when the flag was present on the
// command line.
func NewRootCmd() *cobra.Command {
	flags := &flagSet{}
	root := &cobra.Command{
		Use:   "tiler",
		Short: "Turn directory snapshots into overlay workspaces",
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return prepare(cmd, flags)
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	root.CompletionOptions.DisableDefaultCmd = true
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return usageError{err}
	})
	bindFlags(root, flags)
	root.AddCommand(newVersionCmd())
	root.AddCommand(newDoctorCmd())
	root.AddCommand(newBuildCmd())
	root.AddCommand(newImagesCmd())
	root.AddCommand(newMountCmd())
	root.AddCommand(newUmountCmd())
	return root
}

func prepare(cmd *cobra.Command, flags *flagSet) error {
	silenceRuntimeUsage(cmd)
	patch, err := commandPatch(cmd, flags)
	if err != nil {
		return err
	}
	cfg, err := loadConfig(patch)
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("working directory: %w", err)
	}
	if err := config.OutsideReserved(cwd, cfg); err != nil {
		return err
	}
	remember(cmd, cfg)
	return nil
}

func silenceRuntimeUsage(cmd *cobra.Command) {
	cmd.SilenceUsage = true
	cmd.Root().SilenceUsage = true
}

func loadConfig(patch config.Patch) (config.Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return config.Config{}, err
	}
	if strings.TrimSpace(home) == "" {
		return config.Config{}, errors.New("home directory is empty")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return config.Config{}, fmt.Errorf("working directory: %w", err)
	}
	return config.Load(cwd, home, filesystemRoot, patch)
}

func remember(cmd *cobra.Command, cfg config.Config) {
	root := cmd.Root()
	ctx := context.WithValue(root.Context(), loadedConfigKey{}, cfg)
	root.SetContext(ctx)
}

func loadedConfig(cmd *cobra.Command) (config.Config, bool) {
	cfg, ok := cmd.Root().Context().Value(loadedConfigKey{}).(config.Config)
	return cfg, ok
}
