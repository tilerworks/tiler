package cli

import (
	"errors"

	"github.com/jiangxin/tiler/internal/config"
	"github.com/jiangxin/tiler/internal/image"
	"github.com/jiangxin/tiler/internal/session"
)

// publishVersionAndRebase keeps publication and its reference handoff under
// the published image's lock, acquired before the session lock.
func publishVersionAndRebase(cfg config.Config, mountpoint, name string,
	publish func() (image.ImageMeta, error), fullID func(image.ImageMeta) string,
) (image.ImageMeta, error) {
	if publish == nil || fullID == nil {
		return image.ImageMeta{}, errors.New("version handoff: invalid publication callback")
	}
	hold, err := image.LockImage(cfg.ImagesDir, name, image.DefaultLibraryLockTimeout)
	if err != nil {
		return image.ImageMeta{}, err
	}
	defer hold.Release()
	if err := image.PrepareImageLocked(cfg.ImagesDir, name); err != nil {
		return image.ImageMeta{}, err
	}
	node, err := publish()
	if err != nil {
		return image.ImageMeta{}, err
	}
	err = sessionEngine(cfg).RebaseMountedStackLocked(mountpoint, name, fullID(node), node.ID)
	return node, err
}

// resolveVersionAndPin resolves a selector, then locks that image only while
// the session records the tip. The callback performs only session work.
func resolveVersionAndPin(cfg config.Config, sessionName, selector string,
	pin func(*session.Engine, image.Target) error,
) error {
	if pin == nil {
		return errors.New("version handoff: invalid pin callback")
	}
	target, err := image.ResolveVersionMount(cfg, selector)
	if err != nil {
		return err
	}
	hold, err := image.LockImage(cfg.ImagesDir, target.Name, image.DefaultLibraryLockTimeout)
	if err != nil {
		return err
	}
	defer hold.Release()
	if err := image.PrepareImageLocked(cfg.ImagesDir, target.Name); err != nil {
		return err
	}
	current, err := image.ResolveVersionMount(cfg, selector)
	if err != nil {
		return err
	}
	if current.ID != target.ID || current.Tip != target.Tip {
		return errors.New("version handoff: resolved version changed")
	}
	target = current
	return sessionEngine(cfg).WithVersionSessionLocked(sessionName, func(local *session.Engine) error {
		before, loadErr := local.Load(sessionName)
		if loadErr != nil && !errors.Is(loadErr, session.ErrNotFound) {
			return loadErr
		}
		if err := pin(local, target); err != nil {
			var rollbackErr error
			if errors.Is(loadErr, session.ErrNotFound) {
				_, rollbackErr = local.Discard(sessionName, true)
			} else {
				rollbackErr = local.Save(before)
			}
			return errors.Join(err, rollbackErr)
		}
		return nil
	})
}
