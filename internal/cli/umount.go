package cli

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/jiangxin/tiler/internal/config"
	"github.com/jiangxin/tiler/internal/session"
	"github.com/jiangxin/tiler/internal/workspace"
	"github.com/spf13/cobra"
)

func newUmountCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "umount <dir>",
		Short: "Unmount a workspace by path",
		Long: "Umount tears the view at <dir> down: the merged point\n" +
			"first, then the runtime transit points beneath it, with a\n" +
			"lazy-unmount fallback for busy or stale mounts. It is\n" +
			"idempotent: an unmounted <dir> succeeds silently. Upper and\n" +
			"work data survives, so remounting the same directory reuses\n" +
			"the previous changes.",
		Args: exactOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUmount(cmd, args)
		},
	}
	return cmd
}

func runUmount(cmd *cobra.Command, args []string) error {
	cfg, ok := loadedConfig(cmd)
	if !ok {
		return errors.New("umount: configuration is not loaded")
	}
	dir, err := filepath.Abs(args[0])
	if err != nil {
		return fmt.Errorf("umount: target: %w", err)
	}
	table, err := loadMountTable()
	if err != nil {
		return fmt.Errorf("umount: %w", err)
	}
	entries := toEntries(table)
	removed, err := unmountEngine(workspace.UnmountRequest{
		Dir:        dir,
		Table:      entries,
		RuntimeDir: resolveCombinationRuntime(cfg, dir, entries),
		Owned:      ownedLibrarySource(cfg.ImagesDir),
		Reload:     reloadEntries,
		Run:        mountRun,
	})
	if err != nil {
		return err
	}
	if removed == 0 {
		return nil
	}
	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "unmounted %s\n", dir); err != nil {
		return err
	}
	closeImplicitSession(cmd, cfg, dir)
	return nil
}

// closeImplicitSession closes the implicit auto-* session behind
// the removed view once it was the session's last live view. The
// view is already down, so a failing lookup or close warns instead
// of failing the umount.
func closeImplicitSession(cmd *cobra.Command, cfg config.Config, dir string) {
	engine := sessionEngine(cfg)
	if _, err := engine.CloseImplicitFor(dir, sessionUmount(cfg)); err != nil {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
			"umount: warning: implicit session close: %v\n", err)
	}
}

// reloadEntries re-reads the host mount table through the injected
// seam for the engine's residue sweep.
func reloadEntries() ([]workspace.Entry, error) {
	table, err := loadMountTable()
	if err != nil {
		return nil, err
	}
	return toEntries(table), nil
}

// resolveCombinationRuntime recovers the session-scoped combination
// directory for one mountpoint: prefer the live table entry's
// upper/lower path, then fall back to the covering session record's
// RuntimeID when the overlay entry has already vanished or does not
// expose a recoverable combination path.
func resolveCombinationRuntime(
	cfg config.Config,
	dir string,
	table []workspace.Entry,
) string {
	for i := len(table) - 1; i >= 0; i-- {
		if table[i].Point != dir {
			continue
		}
		if runtime := workspace.CombinationRuntime(table[i]); runtime != "" {
			return runtime
		}
		break
	}
	engine := sessionEngine(cfg)
	hit, err := engine.SessionForPath(dir)
	if err != nil {
		return ""
	}
	for _, action := range hit.Record.Actions {
		if action.Type != session.TypeMount || action.Status == session.StatusUnmounted {
			continue
		}
		if action.Mountpoint != hit.Mountpoint && action.Mountpoint != dir {
			continue
		}
		if runtime := sessionRuntimeDir(cfg, hit.Record.Name, action); runtime != "" {
			return runtime
		}
	}
	return ""
}
