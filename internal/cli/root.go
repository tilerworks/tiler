// Package cli is the only cobra command tree. Startup loads
// configuration and rejects a working directory inside a reserved
// tree. Usage failures map to exit code 2.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jiangxin/tiler/internal/config"
	"github.com/spf13/cobra"
)

// Version is injected by make build and printed by both version entrypoints.
// Direct go builds stay dev unless the caller supplies the same linker flag.
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
		Use:     "tiler",
		Short:   "Turn directory snapshots into overlay workspaces",
		Version: Version,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return prepare(cmd, flags)
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	root.CompletionOptions.DisableDefaultCmd = true
	// Short names (ls, rm) stay invocable through Aliases. Help and
	// completion list only the canonical command, so the Aliases
	// block is omitted from cobra's usage text.
	root.SetUsageTemplate(usageWithoutAliases)
	root.SetVersionTemplate("tiler {{.Version}}\n")
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
	root.AddCommand(newSessionCmd())
	root.AddCommand(newStatusCmd())
	root.AddCommand(newBenchmarkCmd())
	root.AddCommand(newCheckpointCmd())
	return root
}

func prepare(cmd *cobra.Command, flags *flagSet) error {
	silenceRuntimeUsage(cmd)
	if cmd.Name() == "version" {
		return nil
	}
	if cmd.Name() == "benchmark" {
		return rejectBenchmarkRootFlags(cmd)
	}
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
	// Create $HOME/.tiler.yaml on first run so users have a documented,
	// editable starting point. Best-effort: a write failure (for example a
	// read-only home) must not stop tiler, because Load resolves the same
	// defaults without the file.
	_ = config.EnsureUserFile(home)
	// Upgrade an incompatible user file (missing version or removed
	// keys such as runtime_dir and the old split keys) before Load's
	// strict Decode. Must run here so a leftover key never surfaces
	// as "unknown field".
	// When the upgrade itself cannot complete and Load then fails, the
	// upgrade error is reported instead of the opaque parse failure.
	upgradeErr := config.UpgradeUserFile(home)
	cwd, err := os.Getwd()
	if err != nil {
		return config.Config{}, fmt.Errorf("working directory: %w", err)
	}
	// TILER_CEILING_DIR bounds the upward project-config walk so discovery
	// stays inside a sandbox (for example an e2e test workspace) and never
	// picks up a stray .tiler.yaml higher in the tree.
	ceiling := os.Getenv("TILER_CEILING_DIR")
	cfg, err := config.Load(cwd, home, filesystemRoot, ceiling, patch)
	if err != nil && upgradeErr != nil {
		return config.Config{}, fmt.Errorf(
			"upgrade user config %s: %w",
			filepath.Join(home, ".tiler.yaml"), upgradeErr)
	}
	return cfg, err
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

// usageWithoutAliases is cobra's default usage template without the
// Aliases section. Taken from spf13/cobra defaultUsageTemplate.
const usageWithoutAliases = `Usage:{{if .Runnable}}
  {{.UseLine}}{{end}}{{if .HasAvailableSubCommands}}
  {{.CommandPath}} [command]{{end}}{{if .HasExample}}

Examples:
{{.Example}}{{end}}{{if .HasAvailableSubCommands}}{{$cmds := .Commands}}{{if eq (len .Groups) 0}}

Available Commands:{{range $cmds}}{{if (or .IsAvailableCommand (eq .Name "help"))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{else}}{{range $group := .Groups}}

{{.Title}}{{range $cmds}}{{if (and (eq .GroupID $group.ID) (or .IsAvailableCommand (eq .Name "help")))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{end}}{{if not .AllChildCommandsHaveGroup}}

Additional Commands:{{range $cmds}}{{if (and (eq .GroupID "") (or .IsAvailableCommand (eq .Name "help")))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{end}}{{end}}{{end}}{{if .HasAvailableLocalFlags}}

Flags:
{{.LocalFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasAvailableInheritedFlags}}

Global Flags:
{{.InheritedFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasHelpSubCommands}}

Additional help topics:{{range .Commands}}{{if .IsAdditionalHelpTopicCommand}}
  {{rpad .CommandPath .CommandPathPadding}} {{.Short}}{{end}}{{end}}{{end}}{{if .HasAvailableSubCommands}}

Use "{{.CommandPath}} [command] --help" for more information about a command.{{end}}
`
