package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jiangxin/tiler/internal/checkpoint"
	"github.com/spf13/cobra"
)

func newCheckpointCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "checkpoint",
		Short: "Save and restore a directory checkpoint",
	}
	cmd.AddCommand(newCheckpointCreateCmd(), newCheckpointRestoreCmd())
	return cmd
}

func newCheckpointCreateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "create <dir>...",
		Short: "Save the original directory and activate a task copy",
		Long: "Copy the directory and atomically exchange it with the original.\n" +
			"The original becomes <dir>.checkpoint and the copy remains at <dir>.\n" +
			"Clone-on-write is used when available. An existing ordinary\n" +
			".tiler-checkpoint file is removed before copying. Stop writers first:\n" +
			"open directory handles still refer to the original checkpoint.\n" +
			"Multiple directories are processed in order; processing stops on failure.\n" +
			"A mistaken .checkpoint suffix is corrected with a warning.\n" +
			"Interrupted operations can be retried with the same command.",
		Args: checkpointAtLeastOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, ok := loadedConfig(cmd)
			if !ok {
				return errors.New("checkpoint create: configuration is not loaded")
			}
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("checkpoint create: home: %w", err)
			}
			paths, err := checkpointArguments(cmd, args)
			if err != nil {
				return fmt.Errorf("checkpoint create: %w", err)
			}
			for i, target := range paths {
				path, existing, err := checkpoint.CreateWithStatus(target, home, cfg)
				if err != nil {
					return checkpointBatchError("create", target, paths[i+1:], err)
				}
				verb := "created checkpoint"
				if existing {
					verb = "checkpoint already exists"
				}
				if _, err = fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", verb, path); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func newCheckpointRestoreCmd() *cobra.Command {
	var remove bool
	cmd := &cobra.Command{
		Use:   "restore <dir>...",
		Short: "Restore and consume a directory checkpoint",
		Long: "Restore <dir>.checkpoint and consume it. The current directory is\n" +
			"archived with the restore timestamp; --rm deletes the archive after\n" +
			"restoration. A failed transaction can be retried with the same path.\n" +
			"Multiple directories are processed in order; processing stops on failure.\n" +
			"A mistaken .checkpoint suffix is corrected with a warning.\n" +
			"Older v1 checkpoints and unfinished transactions are preserved and\n" +
			"require the older version or manual recovery.",
		Args: checkpointAtLeastOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, ok := loadedConfig(cmd)
			if !ok {
				return errors.New("checkpoint restore: configuration is not loaded")
			}
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("checkpoint restore: home: %w", err)
			}
			paths, err := checkpointArguments(cmd, args)
			if err != nil {
				return fmt.Errorf("checkpoint restore: %w", err)
			}
			for i, target := range paths {
				_, missing, err := checkpoint.RestoreWithStatus(target, home, cfg, remove)
				if err != nil {
					return checkpointBatchError("restore", target, paths[i+1:], err)
				}
				if missing {
					if _, err = fmt.Fprintf(cmd.ErrOrStderr(), "warning: checkpoint missing for %s\n", target); err != nil {
						return err
					}
					continue
				}
				if _, err = fmt.Fprintf(cmd.OutOrStdout(), "restored %s\n", target); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&remove, "rm", false, "remove the archived directory after restore")
	return cmd
}

// checkpointArguments resolves the whole batch before any transaction starts.
func checkpointArguments(cmd *cobra.Command, args []string) ([]string, error) {
	paths := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "" {
			return nil, errors.New("empty directory argument")
		}
		path := filepath.Clean(arg)
		if strings.HasSuffix(path, ".checkpoint") {
			candidate := strings.TrimSuffix(path, ".checkpoint")
			info, err := os.Stat(candidate)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, fmt.Errorf("inspect %s: %w", candidate, err)
			}
			if err == nil && info.IsDir() {
				if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "warning: using %s instead of %s\n", candidate, arg); err != nil {
					return nil, err
				}
				path = candidate
			}
		}
		resolved, err := checkpoint.Resolve(path)
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", path, err)
		}
		for _, previous := range paths {
			if checkpointPathsOverlap(previous, resolved) {
				return nil, fmt.Errorf("overlapping checkpoint targets: %s and %s", previous, resolved)
			}
		}
		paths = append(paths, resolved)
	}
	return paths, nil
}

func checkpointPathsOverlap(a, b string) bool {
	within := func(parent, child string) bool {
		rel, err := filepath.Rel(parent, child)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	return within(a, b) || within(b, a)
}

func checkpointBatchError(operation, target string, remaining []string, err error) error {
	if len(remaining) == 0 {
		return fmt.Errorf("checkpoint %s: failed %s: %w", operation, target, err)
	}
	return fmt.Errorf("checkpoint %s: failed %s: %w; not processed: %s", operation, target, err, strings.Join(remaining, ", "))
}

func checkpointAtLeastOneArg(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return usageError{fmt.Errorf("%s requires at least one directory", cmd.CommandPath())}
	}
	return nil
}
