package cli

import (
	"fmt"

	"github.com/jiangxin/tiler/internal/config"
	"github.com/jiangxin/tiler/internal/image"
	"github.com/jiangxin/tiler/internal/session"
	"github.com/jiangxin/tiler/internal/workspace"
)

// versionMountRequest prepares a v3 mount or replay request from the exact
// immutable identity recorded in the session action.
func versionMountRequest(
	cfg config.Config,
	action session.Action,
	dir string,
	flags mountFlags,
	runtimeDir string,
) (workspace.Request, error) {
	stack, err := action.VersionStack(cfg.ImagesDir, true)
	if err != nil {
		return workspace.Request{}, err
	}
	members := make([]workspace.MountMember, 0, len(stack))
	for _, member := range stack {
		path, err := materializeVersionMember(cfg, action.Name, member)
		if err != nil {
			return workspace.Request{}, err
		}
		members = append(members, workspace.MountMember{
			Path: path, FSName: member.Path, Format: member.Meta.Format.Type,
		})
	}
	table, err := mountTableOrEmpty()
	if err != nil {
		return workspace.Request{}, fmt.Errorf("mount: %w", err)
	}
	return workspace.Request{
		Members: members, ReadOnly: flags.readOnly, PruneUpper: flags.pruneUpper,
		Subdir: flags.subdir, Table: toEntries(table),
		SquashfsOptions: cfg.SquashfsOptions, FuseOptions: cfg.FuseOptions,
		RuntimeDir: runtimeDir, MergedDir: dir, Run: mountRun,
	}, nil
}

func runVersionMountEngine(
	cfg config.Config,
	action session.Action,
	dir string,
	flags mountFlags,
	runtimeDir string,
) (session.Result, error) {
	req, err := versionMountRequest(cfg, action, dir, flags, runtimeDir)
	if err != nil {
		return session.Result{}, err
	}
	for _, member := range req.Members {
		if err := gateFormatTools("mount", member.Format); err != nil {
			return session.Result{}, err
		}
	}
	if err := gateAnyStack("mount"); err != nil {
		return session.Result{}, err
	}
	return runEngine(req)
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
