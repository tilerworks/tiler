package session

import "github.com/jiangxin/tiler/internal/mountinfo"

// Identity resolves image identities from the session records: the
// mount action of an active session that recorded the point carries
// the owner and repository of the library image behind it. It is
// the fallback for the mount table's path identity when a killed
// daemon left the table entry anonymous or stale. File-path images
// record no identity and never resolve.
type Identity struct {
	// Store reads the session records.
	Store
}

// Lookup returns the image identity behind a mount point. The
// point is compared with SameMountPoint: hdiutil info resolves a
// symlink such as /tmp, and the record stores filepath.Abs. A
// record another process holds is skipped this round, which reads
// as an unresolved identity.
func (i Identity) Lookup(mountPoint string) (mountinfo.Name, bool, error) {
	var (
		found mountinfo.Name
		hit   bool
	)
	err := i.eachRecord(func(rec Record) bool {
		if rec.State != StateActive {
			return false
		}
		for _, action := range rec.Actions {
			if action.Type != TypeMount || action.Status == StatusUnmounted ||
				!SameMountPoint(action.Mountpoint, mountPoint) {
				continue
			}
			if action.Owner != "" && action.Repo != "" {
				found = mountinfo.Name{Owner: action.Owner, Repo: action.Repo}
				hit = true
			}
			return true
		}
		return false
	})
	if err != nil {
		return mountinfo.Name{}, false, err
	}
	return found, hit, nil
}

// CombinedIdentity settles an image identity through two sources:
// the primary first, the fallback when the primary resolves
// nothing. The build command combines the mount table's path
// identity with the session records, so a view whose daemon died —
// anonymous in the table but present in the record — still names
// its image. A primary error aborts; the fallback never runs for a
// successful primary hit.
type CombinedIdentity struct {
	// Primary is consulted first.
	Primary mountinfo.IdentitySource
	// Fallback answers when the primary resolves nothing.
	Fallback mountinfo.IdentitySource
}

// Lookup resolves the mount point through the primary source, then
// the fallback.
func (c CombinedIdentity) Lookup(mountPoint string) (mountinfo.Name, bool, error) {
	name, ok, err := c.Primary.Lookup(mountPoint)
	if err != nil || ok {
		return name, ok, err
	}
	return c.Fallback.Lookup(mountPoint)
}
