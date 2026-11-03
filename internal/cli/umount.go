package cli

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/jiangxin/tiler/internal/config"
	"github.com/jiangxin/tiler/internal/mountinfo"
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
	if errors.Is(err, mountinfo.ErrUnavailable) && readHdiutilInfo != nil {
		return umountDisk(cmd, cfg, dir)
	}
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
	closeRemovableSession(cmd, cfg, dir)
	return nil
}

// umountDisk detaches one volume through hdiutil info when this
// host has no mount table. A point info does not list is already
// down, so the command stays idempotent and leaves the shadow file.
func umountDisk(cmd *cobra.Command, cfg config.Config, dir string) error {
	images, err := diskImagesFromInfo(readHdiutilInfo)
	if err != nil {
		return fmt.Errorf("umount: %w", err)
	}
	listed := false
	for _, image := range images {
		if session.SameMountPoint(image.Point, dir) {
			listed = true
			break
		}
	}
	if !listed {
		return nil
	}
	if err := detachDiskMount(dir); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "unmounted %s\n", dir); err != nil {
		return err
	}
	closeRemovableSession(cmd, cfg, dir)
	return nil
}

// closeRemovableSession writebacks StatusUnmounted for the covering
// session, then closes a --rm session once the removed view was its
// last live one. Non--rm sessions stay active. The view is already
// down, so a failing release or close warns instead of failing the
// umount.
func closeRemovableSession(cmd *cobra.Command, cfg config.Config, dir string) {
	engine := sessionEngine(cfg)
	if err := engine.ReleaseMount(dir); err != nil {
		_, _ = writeWarning(cmd.ErrOrStderr(), "umount", "%q was unmounted, but its session mount record could not be released: %v\n", dir, err)
	}
	if _, err := engine.CloseRemovableFor(dir, sessionUmount(cfg)); err != nil {
		_, _ = writeWarning(cmd.ErrOrStderr(), "umount", "%q was unmounted, but automatic session cleanup failed: %v\n", dir, err)
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
// upper/lower path, then the covering session record's RuntimeID
// when the overlay entry has already vanished or does not expose a
// recoverable combination path (fuse-overlayfs stays anonymous once
// its daemon is gone). The store scan is lock-free so session close,
// which already holds the advisory lock, can still recover the path
// and sweep stale lowers that SessionForPath would skip.
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
	if runtime := runtimeFromSessionLookup(cfg, dir); runtime != "" {
		return runtime
	}
	return runtimeFromStore(cfg, dir, table)
}

// runtimeFromSessionLookup asks SessionForPath for the covering
// record. It fails closed when the record's scan lock is held.
func runtimeFromSessionLookup(cfg config.Config, dir string) string {
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

// runtimeFromStore reads session records without the advisory lock
// and returns sessions/<name>/runtime/m<seq> for an active mount at
// dir. Close holds that lock while tearing views down, so the locked
// SessionForPath scan cannot see the record under close. When more
// than one active record still claims dir, prefer a runtime that
// still has a mount listed under it in the table so a remount into
// a new session does not sweep the wrong combination.
func runtimeFromStore(cfg config.Config, dir string, table []workspace.Entry) string {
	if cfg.SessionsDir == "" || dir == "" {
		return ""
	}
	records, err := session.Store{Dir: cfg.SessionsDir}.List()
	if err != nil {
		return ""
	}
	fallback := ""
	for _, rec := range records {
		if rec.State != session.StateActive {
			continue
		}
		for _, action := range rec.Actions {
			if action.Type != session.TypeMount ||
				action.Status == session.StatusUnmounted ||
				action.Mountpoint != dir {
				continue
			}
			runtime := sessionRuntimeDir(cfg, rec.Name, action)
			if runtime == "" {
				continue
			}
			if runtimeListed(table, runtime) {
				return runtime
			}
			if fallback == "" {
				fallback = runtime
			}
		}
	}
	return fallback
}

// runtimeListed reports whether the mount table still lists any
// point at or under the combination directory.
func runtimeListed(table []workspace.Entry, runtime string) bool {
	runtime = filepath.Clean(runtime)
	for _, entry := range table {
		point := filepath.Clean(entry.Point)
		if point == runtime || workspaceUnderPrefix(point, runtime) {
			return true
		}
	}
	return false
}

// workspaceUnderPrefix reports whether path is a proper child of
// root. Mirrors workspace.underPrefix without exporting it.
func workspaceUnderPrefix(path, root string) bool {
	path, root = filepath.Clean(path), filepath.Clean(root)
	if root == "" || root == "." || root == string(filepath.Separator) {
		return false
	}
	prefix := root + string(filepath.Separator)
	return len(path) > len(prefix) && path[:len(prefix)] == prefix
}
