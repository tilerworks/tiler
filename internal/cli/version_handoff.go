package cli

import (
	"errors"

	"github.com/jiangxin/tiler/internal/config"
	"github.com/jiangxin/tiler/internal/image"
	"github.com/jiangxin/tiler/internal/session"
)

// publishVersionAndRebase keeps publication and its reference handoff in one
// library critical section. Commands switch to this seam with the v3 runtime.
func publishVersionAndRebase(cfg config.Config, mountpoint, name string,
	publish func() (image.ImageMeta, error), fullID func(image.ImageMeta) string,
) (image.ImageMeta, error) {
	if publish == nil || fullID == nil {
		return image.ImageMeta{}, errors.New("version handoff: invalid publication callback")
	}
	release, err := image.LockLibrary(cfg.ImagesDir, image.DefaultLibraryLockTimeout)
	if err != nil {
		return image.ImageMeta{}, err
	}
	defer release()
	node, err := publish()
	if err != nil {
		return image.ImageMeta{}, err
	}
	err = sessionEngine(cfg).RebaseMountedStackLocked(mountpoint, name, fullID(node), node.ID)
	return node, err
}

// resolveVersionAndPin keeps selector resolution stable until the session has
// durably recorded that exact tip. The callback performs only session work.
func resolveVersionAndPin(cfg config.Config, sessionName, selector string,
	pin func(*session.Engine, image.Target) error,
) error {
	if pin == nil {
		return errors.New("version handoff: invalid pin callback")
	}
	release, err := image.LockLibrary(cfg.ImagesDir, image.DefaultLibraryLockTimeout)
	if err != nil {
		return err
	}
	defer release()
	target, err := image.ResolveVersionMount(cfg, selector)
	if err != nil {
		return err
	}
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
