package session

// annotateDiskDetails uses the same snapshot and identity rules as disk health.
// This host-independent seam allows Darwin-shaped snapshots in unit tests.
func (e *Engine) annotateDiskDetails(name string, mounts []MountView, images []DiskImage) {
	for i := range mounts {
		mount := &mounts[i]
		if mount.Status == StatusUnmounted {
			continue
		}
		for _, attached := range images {
			if !e.diskMatch(name, mount.Action, []DiskImage{attached}) {
				continue
			}
			mount.DiskImage, mount.Shadow = attached.Image, attached.Shadow
			// A disk image has one attached volume, without overlay lowers.
			if len(mount.Layers) == 1 {
				mount.Layers[0].Mountpoint = attached.Point
				mount.Layers[0].Available = true
			}
			break
		}
	}
}
