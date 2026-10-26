//go:build darwin

package benchmark

import (
	"errors"
	"fmt"

	"github.com/jiangxin/tiler/internal/container"
)

func platformAttach(cmd Command, _ Host, typ, image, point string) (Mount, error) {
	if typ != "dmg" {
		return Mount{}, fmt.Errorf("benchmark: %s attach is not available on darwin", typ)
	}
	return attachDMG(cmd, image, point)
}

func attachDMG(cmd Command, image, point string) (Mount, error) {
	argv, err := container.HdiutilAttachReadOnlyArgv(image, point)
	if err != nil {
		return Mount{}, err
	}
	if err := container.RunHdiutil(toolRunner(cmd), argv, nil); err != nil {
		return Mount{}, fmt.Errorf("benchmark: attach dmg: %w", err)
	}
	return Mount{Tool: "hdiutil"}, nil
}

func platformDetach(cmd Command, m Mount, point string) error {
	if m.Tool != "hdiutil" {
		return fmt.Errorf("benchmark: cannot detach %q on darwin", m.Tool)
	}
	return detachDMG(cmd, point)
}

// detachDMG tries hdiutil detach with no -force. diskutil eject
// runs only after that detach fails, and a successful eject clears
// the detach error.
func detachDMG(cmd Command, point string) error {
	argv, err := container.HdiutilDetachArgv(point)
	if err != nil {
		return err
	}
	detachErr := container.RunHdiutil(toolRunner(cmd), argv, nil)
	if detachErr == nil {
		return nil
	}
	ejectErr := ejectDMG(cmd, point)
	if ejectErr == nil {
		return nil
	}
	return fmt.Errorf("benchmark: detach %s: %w", point, errors.Join(detachErr, ejectErr))
}

func ejectDMG(cmd Command, point string) error {
	_, err := cmd("diskutil", nil, "eject", point)
	if err != nil {
		return fmt.Errorf("benchmark: diskutil eject %s: %w", point, err)
	}
	return nil
}
