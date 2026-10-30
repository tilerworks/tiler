package cli

import (
	"fmt"

	"github.com/jiangxin/tiler/internal/activity"
	"github.com/jiangxin/tiler/internal/config"
	"github.com/jiangxin/tiler/internal/image"
	"github.com/jiangxin/tiler/internal/session"
	"github.com/jiangxin/tiler/internal/workspace"
)

// versionMountRequest prepares a v3 mount or replay request from the
// desired identity recorded on the session action (full id and tip id).
// A served override names the stack a still-healthy view provides; a
// replay mounts the desired stack, so a rebased lower is what the engine
// attaches. The validated desired stack travels back with the request, and
// a real remount records activity for that same chain. A healthy open that
// does not remount records the served stack through
// recordHealthyMountActivity.
func versionMountRequest(
	cfg config.Config,
	action session.Action,
	dir string,
	flags mountFlags,
	runtimeDir string,
) (workspace.Request, []session.VersionMember, error) {
	stack, err := action.VersionStack(cfg.ImagesDir, false)
	if err != nil {
		return workspace.Request{}, nil, err
	}
	members := make([]workspace.MountMember, 0, len(stack))
	for _, member := range stack {
		path, err := materializeVersionMember(cfg, action.Name, member)
		if err != nil {
			return workspace.Request{}, nil, err
		}
		members = append(members, workspace.MountMember{
			Path: path, FSName: member.Path, Format: member.Meta.Format.Type,
		})
	}
	table, err := mountTableOrEmpty()
	if err != nil {
		return workspace.Request{}, nil, fmt.Errorf("mount: %w", err)
	}
	return workspace.Request{
		Members: members, ReadOnly: flags.readOnly, PruneUpper: flags.pruneUpper,
		Subdir: flags.subdir, Table: toEntries(table),
		SquashfsOptions: cfg.SquashfsOptions, FuseOptions: cfg.FuseOptions,
		RuntimeDir: runtimeDir, MergedDir: dir, Run: mountRun,
		Progress: mountNotice,
	}, stack, nil
}

// runVersionMountEngine executes one library mount or replay. The mount
// command and the session replay share this half, and both already hold the
// library lock the image activity batch needs: the command pins the selector
// under it and the replay runs inside the session's version scope.
func runVersionMountEngine(
	cfg config.Config,
	name string,
	action session.Action,
	dir string,
	flags mountFlags,
	runtimeDir string,
) (session.Result, error) {
	req, stack, err := versionMountRequest(cfg, action, dir, flags, runtimeDir)
	if err != nil {
		return session.Result{}, err
	}
	// The started record lands before the toolchain gates, so a refused
	// mount still leaves the attempted phase behind.
	if err := recordVersionStackActivity(cfg, name, action, stack, activity.PhaseStarted, nil); err != nil {
		return session.Result{}, err
	}
	result, mountErr := runVersionMount(req)
	phase := activity.PhaseSucceeded
	if mountErr != nil {
		phase = activity.PhaseFailed
	}
	logErr := recordVersionStackActivity(cfg, name, action, stack, phase, mountErr)
	if logErr == nil {
		return result, mountErr
	}
	if mountErr != nil {
		return result, fmt.Errorf("%w (activity: %v)", mountErr, logErr)
	}
	// The view is up and only its record is missing. The session layer
	// keeps a mounted action for ErrActivityFailed instead of writing a
	// live view down as failed.
	return result, fmt.Errorf("%w: mount activity of %s: %w",
		session.ErrActivityFailed, action.Name, logErr)
}

// runVersionMount gates the toolchain of one assembled request and runs it.
func runVersionMount(req workspace.Request) (session.Result, error) {
	needsFUSE := false
	for _, member := range req.Members {
		needsFUSE = needsFUSE || member.Format != image.FormatDMG
		if err := gateFormatTools("mount", member.Format); err != nil {
			return session.Result{}, err
		}
	}
	if needsFUSE {
		if err := gateAnyStack("mount"); err != nil {
			return session.Result{}, err
		}
	}
	return runEngine(req)
}

// recordVersionStackActivity writes one phase of one library mount to the
// activity logs of the stack it serves. The tip records the mount event with
// the action's session, sequence, and mountpoint; every ancestor records one
// dependency record naming the tip. The whole chain is written in one
// traversal, so neither the tip nor an ancestor is propagated twice. Both
// callers hold the library pin of the stack they pass: the mount engine
// resolved one, and the healthy-open hook returns before this function when
// the record carries none. A file mount therefore never reaches this point.
func recordVersionStackActivity(
	cfg config.Config,
	name string,
	action session.Action,
	stack []session.VersionMember,
	phase string,
	opErr error,
) error {
	repo, err := image.OpenRepo(cfg.ImagesDir, action.Name)
	if err != nil {
		return fmt.Errorf("mount: activity %s: %w", action.Name, err)
	}
	repo.LogsDir = cfg.LogsDir
	chain := make([]image.ImageMeta, len(stack))
	for i, member := range stack {
		chain[i] = member.Meta
	}
	ev := activity.Event{
		Event:      activity.EventMount,
		Phase:      phase,
		Session:    name,
		Seq:        action.Seq,
		Mountpoint: action.Mountpoint,
	}
	if opErr != nil {
		ev.Error = opErr.Error()
	}
	tip := chain[len(chain)-1].ID
	if err := image.AppendVersionStackActivityLocked(repo, tip, chain, ev, nil); err != nil {
		return fmt.Errorf("mount: activity %s: %w", action.Name, err)
	}
	return nil
}

func materializeVersionMember(
	cfg config.Config,
	name string,
	member session.VersionMember,
) (string, error) {
	if len(member.Meta.Blocks) != 0 {
		repo, err := image.OpenRepo(cfg.ImagesDir, name)
		if err != nil {
			return "", err
		}
		blocks := make([]image.BlockFile, 0, len(member.Meta.Blocks))
		for _, block := range member.Meta.Blocks {
			path, err := repo.ObjectBlockPath(member.Meta.ID, block.Name)
			if err != nil {
				return "", err
			}
			blocks = append(blocks, image.BlockFile{Path: path, Name: block.Name,
				Size: block.Size, MtimeUnix: block.MtimeUnix, SHA256: block.SHA256})
		}
		return assembleBlocks(cfg.CacheDir, blocks, member.Meta.SHA256, member.Meta.Size)
	}
	path, err := image.ResolvePayloadBytesPath(member.Path)
	if err != nil {
		return "", fmt.Errorf("mount: %w", err)
	}
	if !cfg.CacheEnabled {
		return path, nil
	}
	cached, _, err := ensureCache(cfg.CacheDir, path, member.Meta.SHA256)
	if err != nil {
		return "", fmt.Errorf("mount: %w", err)
	}
	return cached, nil
}
