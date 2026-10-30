package session

import (
	"fmt"
	"path/filepath"

	"github.com/jiangxin/tiler/internal/mountinfo"
)

// MountView pairs a recorded mount with its observed runtime details.
type MountView struct {
	ActionView
	Layers        []LayerView
	OverlayUpper  string
	OverlayLowers []string
	DiskImage     string
	Shadow        string
	Diagnostics   []string
}

// LayerView describes one immutable image in full-to-tip order.
type LayerView struct {
	Image, Version, Kind, Mountpoint string
	Available                        bool
}

func (e *Engine) mountDetails(view View, table []mountinfo.Mount) []MountView {
	var mounts []MountView
	for _, action := range view.Actions {
		if action.Type != TypeMount {
			continue
		}
		mount := MountView{ActionView: action}
		members := []VersionMember{{Path: action.Image}}
		if action.Name != "" {
			var err error
			members, err = action.VersionStack(e.ImagesDir, action.Health == HealthHealthy)
			if err != nil {
				mount.Diagnostics = append(mount.Diagnostics, err.Error())
			}
		}
		for i, member := range members {
			layer := LayerView{Image: action.Name, Version: member.Meta.ID, Kind: member.Meta.Kind}
			if action.Name == "" {
				layer.Image, layer.Version, layer.Kind = action.Image, action.TipID, "full"
			}
			point := filepath.Join(e.SessionsDir, view.Name, "runtime", action.RuntimeID, fmt.Sprintf("lower-%d", len(members)-1-i))
			if len(members) == 1 && action.Mode == ModeRO && action.Subdir == "" {
				point = action.Mountpoint
			}
			for _, entry := range table {
				if SameMountPoint(entry.Point, point) && SameMountPoint(entry.Source, member.Path) {
					layer.Mountpoint, layer.Available = entry.Point, true
				}
			}
			mount.Layers = append(mount.Layers, layer)
		}
		for _, entry := range table {
			if SameMountPoint(entry.Point, action.Mountpoint) && SameMountPoint(viewRuntimeDir(entry), filepath.Join(e.SessionsDir, view.Name, "runtime", action.RuntimeID)) {
				mount.OverlayUpper, mount.OverlayLowers = entry.Upper, append([]string(nil), entry.Lowers...)
			}
		}
		mounts = append(mounts, mount)
	}
	return mounts
}
