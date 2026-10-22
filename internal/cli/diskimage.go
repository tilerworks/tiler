package cli

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/jiangxin/tiler/internal/container"
	"github.com/jiangxin/tiler/internal/mountinfo"
	"github.com/jiangxin/tiler/internal/session"
	"github.com/jiangxin/tiler/internal/workspace"
)

// readHdiutilInfo runs hdiutil info -plist. Nil means this host
// reconciles sessions through the mount table. The darwin file sets
// the real reader. Tests replace it with a plist fixture and must
// not execute /usr/bin/hdiutil.
var readHdiutilInfo func() ([]byte, error)

// runHdiutil executes one hdiutil vector and returns stdout. Nil
// selects the platform exec inside the workspace. Tests install a
// recorder.
var runHdiutil func(argv []string, consErr error) ([]byte, error)

// attachedDMG reports whether dir contains an image hdiutil info
// still has attached. A nil reader means this host has no disk-image
// table, so nothing is attached. The check looks at image paths
// only; a session shadow is not an image file.
func attachedDMG(dir string) (bool, error) {
	if readHdiutilInfo == nil {
		return false, nil
	}
	images, err := diskImagesFromInfo(readHdiutilInfo)
	if err != nil {
		return false, err
	}
	for _, img := range images {
		if img.Image != "" && pathWithin(img.Image, dir) {
			return true, nil
		}
	}
	return false, nil
}

func pathWithin(path, dir string) bool {
	path, dir = filepath.Clean(path), filepath.Clean(dir)
	if path == dir {
		return true
	}
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// diskImagesFromInfo parses one hdiutil info -plist snapshot into
// the session reconciliation model.
func diskImagesFromInfo(read func() ([]byte, error)) ([]session.DiskImage, error) {
	if read == nil {
		return nil, errors.New("hdiutil info reader is nil")
	}
	out, err := read()
	if err != nil {
		return nil, err
	}
	parsed, err := container.ParseHdiutilInfo(out)
	if err != nil {
		return nil, err
	}
	images := make([]session.DiskImage, 0, len(parsed))
	for _, item := range parsed {
		images = append(images, session.DiskImage{
			Point:  item.MountPoint,
			Image:  item.ImagePath,
			Shadow: item.ShadowPath,
		})
	}
	return images, nil
}

// detachDiskMount tears down one dmg volume when the host has no
// mount table. The recorded mount point is filepath.Abs, while
// hdiutil info reports that directory with symlinks resolved, so
// the lookup compares the resolved paths. A volume info does not
// list is already gone. The shadow file is left for the caller: a
// plain close keeps it, and --rm deletes the runtime afterwards.
func detachDiskMount(mountpoint string) error {
	if readHdiutilInfo == nil {
		return fmt.Errorf("session: %w", mountinfo.ErrUnavailable)
	}
	images, err := diskImagesFromInfo(readHdiutilInfo)
	if err != nil {
		return fmt.Errorf("session: %w", err)
	}
	var match session.DiskImage
	listed := false
	for _, image := range images {
		if session.SameMountPoint(image.Point, mountpoint) {
			match = image
			listed = true
			break
		}
	}
	if !listed {
		return nil
	}
	_, err = unmountEngine(workspace.UnmountRequest{
		Dir: mountpoint,
		Table: []workspace.Entry{{
			Point:  mountpoint,
			FSType: "dmg",
			Source: match.Image,
			Upper:  match.Shadow,
		}},
		Hdiutil: runHdiutil,
	})
	return err
}
