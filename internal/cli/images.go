package cli

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/jiangxin/tiler/internal/config"
	"github.com/jiangxin/tiler/internal/image"
	"github.com/jiangxin/tiler/internal/session"
	"github.com/spf13/cobra"
)

func newImagesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "images",
		Short: "Manage the local image library",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newImagesListCmd())
	cmd.AddCommand(newImagesGCCmd())
	cmd.AddCommand(newImagesRemoveCmd())
	return cmd
}

func newImagesListCmd() *cobra.Command {
	var archives bool
	cmd := &cobra.Command{
		Use:   "ls [owner/repo]",
		Short: "List active images",
		Long: "Without a name, list every active image in the library. With a\n" +
			"name, also list each incremental chain id. --archives adds the\n" +
			"archive and held chain ids.",
		Args: maxOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, ok := loadedConfig(cmd)
			if !ok {
				return errors.New("images ls: configuration is not loaded")
			}
			out := cmd.OutOrStdout()
			if len(args) == 1 {
				return listOneImage(out, cfg, args[0], archives)
			}
			return listAllImages(out, cfg, archives)
		},
	}
	cmd.Flags().BoolVar(&archives, "archives", false, "include archive and held chain ids")
	return cmd
}

func listAllImages(w io.Writer, cfg config.Config, archives bool) error {
	repos, err := image.ListRepos(cfg.ImagesDir)
	if err != nil {
		return fmt.Errorf("images ls: %w", err)
	}
	for _, repo := range repos {
		state, err := image.Inspect(repo)
		if err != nil {
			return fmt.Errorf("images ls: %s/%s: %w", repo.Owner, repo.Repo, err)
		}
		if !state.HasActive {
			continue
		}
		if err := writeImageLine(w, repo.Owner+"/"+repo.Repo, state); err != nil {
			return err
		}
		if archives {
			if err := writeSideChains(w, state); err != nil {
				return err
			}
		}
	}
	return nil
}

func listOneImage(w io.Writer, cfg config.Config, name string, archives bool) error {
	repo, err := image.OpenRepo(cfg.ImagesDir, name)
	if err != nil {
		return usageError{err}
	}
	state, err := image.Inspect(repo)
	if err != nil {
		return fmt.Errorf("images ls: %w", err)
	}
	if !state.HasActive {
		return fmt.Errorf("images ls: %s has no active image", name)
	}
	if err := writeImageLine(w, name, state); err != nil {
		return err
	}
	for _, entry := range state.Active.Chain {
		if _, err := fmt.Fprintf(w, "  incr %s\n", entry.ID); err != nil {
			return err
		}
	}
	if archives {
		return writeSideChains(w, state)
	}
	return nil
}

func writeImageLine(w io.Writer, name string, state image.State) error {
	line := fmt.Sprintf("%s %s chain=%d", name, state.Active.ID, len(state.Active.Chain))
	if state.Active.Deprecated {
		line += " deprecated"
	}
	_, err := fmt.Fprintln(w, line)
	return err
}

func writeSideChains(w io.Writer, state image.State) error {
	for _, id := range state.Archives {
		if _, err := fmt.Fprintf(w, "  archive %s\n", id); err != nil {
			return err
		}
	}
	for _, id := range state.Holds {
		if _, err := fmt.Fprintf(w, "  hold %s\n", id); err != nil {
			return err
		}
	}
	return nil
}

func newImagesGCCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "gc",
		Short: "Promote held chains, expire archives, and clean session records",
		Long: "One garbage collection entry manages two kinds of expired\n" +
			"objects: it promotes every unreferenced image hold into\n" +
			"archives, deletes the archives past the retention rules, and\n" +
			"then cleans the session records — deleting closed records past\n" +
			"the session age limit and reporting active sessions whose\n" +
			"mounts are all stale. Active session records are never\n" +
			"deleted automatically.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, ok := loadedConfig(cmd)
			if !ok {
				return errors.New("images gc: configuration is not loaded")
			}
			now := time.Now()
			refs := session.Refs{Dir: cfg.SessionsDir}
			events, gcErr := image.GC(cfg, refs, now)
			out := cmd.OutOrStdout()
			for _, event := range events {
				if _, wErr := fmt.Fprintf(out, "%s %s/%s %s\n",
					event.Action, event.Owner, event.Repo, event.FullID); wErr != nil {
					return wErr
				}
			}
			cleaned, cleanErr := sessionEngine(cfg).Cleanup(
				cfg.SessionMaxAge, cfg.SessionAutocloseStale, now)
			for _, event := range cleaned {
				if _, wErr := fmt.Fprintf(out, "%s session %s\n",
					event.Action, event.Session); wErr != nil {
					return wErr
				}
			}
			if err := errors.Join(gcErr, cleanErr); err != nil {
				return fmt.Errorf("images gc: %w", err)
			}
			return nil
		},
	}
}

func newImagesRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <owner/repo | owner/repo/archives/full-id>",
		Short: "Deprecate and then remove an image target",
		Long: "The first call flags the target deprecated; the second call\n" +
			"physically removes it. Both calls refuse while a session still\n" +
			"references the target.",
		Args: exactOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, ok := loadedConfig(cmd)
			if !ok {
				return errors.New("images rm: configuration is not loaded")
			}
			target := args[0]
			outcome, err := image.RemoveTarget(cfg, target,
				session.Refs{Dir: cfg.SessionsDir})
			if err != nil {
				if isTargetUsage(err) {
					return usageError{err}
				}
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", outcome, target)
			return err
		},
	}
}

// isTargetUsage reports whether err rejects the target shape itself,
// which is a command-line usage problem, not a runtime failure.
func isTargetUsage(err error) bool {
	return errors.Is(err, image.ErrInvalidName) || errors.Is(err, image.ErrInvalidPath)
}
