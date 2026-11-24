//go:build linux || darwin

package checkpoint

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"syscall"

	"github.com/jiangxin/tiler/internal/durable"
	"golang.org/x/sys/unix"
)

// checkOriginalMarker locates the original by recorded identity and verifies
// its marker before a transition that still requires the marker to exist.
func checkOriginalMarker(p *Parent, r *exchangeRecord, permitted ...string) error {
	base := filepath.Base(r.Target)
	names := []string{base, base + ".checkpoint", base + ".checkpoint.lock"}
	found := false
	for _, name := range names {
		f, err := openMaybe(p, name)
		if err != nil {
			return err
		}
		if f == nil {
			continue
		}
		if matches(f, r.Original) {
			allowed := false
			for _, candidate := range permitted {
				if name == candidate {
					allowed = true
				}
			}
			if !allowed {
				closeMaybe(f)
				return errors.New("original directory is at an unexpected path")
			}
			m, e := readExchangeMarker(f, r.Target)
			closeMaybe(f)
			if e != nil {
				return e
			}
			if found || !m.CreatedAt.Equal(r.CreatedAt) {
				return errors.New("original marker conflicts with transaction")
			}
			found = true
		} else {
			marker, e := readExchangeMarker(f, r.Target)
			closeMaybe(f)
			if e == nil && marker.CreatedAt.Equal(r.CreatedAt) {
				return errors.New("duplicate original marker")
			}
		}
	}
	if !found {
		return errors.New("original directory or marker missing")
	}
	return nil
}

// checkPlaceholderFinalState guards marker removal and completion against
// newly occupied sibling paths and a changed archive.
func checkPlaceholderFinalState(p *Parent, r *exchangeRecord, checkpoint, placeholder, archive string) error {
	for _, path := range []string{checkpoint, placeholder} {
		if err := requireArchiveAbsent(p, path); err != nil {
			return fmt.Errorf("unexpected sibling %s: %w", path, err)
		}
	}
	f, err := openMaybe(p, archive)
	if err != nil {
		return err
	}
	valid := (r.Remove && f == nil) || (!r.Remove && matches(f, r.Copy))
	closeMaybe(f)
	if !valid {
		return errors.New("archive state changed")
	}
	return nil
}

func runPlaceholderRestore(p *Parent, name string, r *exchangeRecord) error {
	base := filepath.Base(r.Target)
	checkpoint := base + ".checkpoint"
	placeholder := checkpoint + ".lock"
	archive := filepath.Base(r.Archive)
	for {
		if err := p.Verify(); err != nil {
			return err
		}
		switch r.Phase {
		case "prepared":
			if err := checkOriginalMarker(p, r, checkpoint); err != nil {
				return err
			}
			if err := checkExchangePair(p, base, checkpoint, r.Copy, r.Original); err != nil {
				return err
			}
			if err := requireArchiveAbsent(p, archive); err != nil {
				return err
			}
			if r.Placeholder.Inode == 0 {
				if err := requireArchiveAbsent(p, placeholder); err != nil {
					return fmt.Errorf("placeholder %s: %w", placeholder, err)
				}
				if err := exchangeOperation("restore:mkdir", func() error { return unix.Mkdirat(int(p.File.Fd()), placeholder, 0700) }); err != nil {
					return err
				}
				f, err := openEntry(p.File, placeholder, syscall.O_RDONLY|syscall.O_DIRECTORY)
				if err != nil {
					return err
				}
				id, err := identity(f)
				closeMaybe(f)
				if err != nil {
					return err
				}
				r.Placeholder = id
				if err = saveExchange(p, name, r); err != nil {
					return err
				}
				if err = exchangeOperation("sync:parent", func() error { return durable.Sync(p.File) }); err != nil {
					return err
				}
			}
			if err := exchangeTransition(p, name, r, "reserve"); err != nil {
				return err
			}
		case "reserve":
			empty, err := openMaybe(p, placeholder)
			if err != nil {
				return err
			}
			if matches(empty, r.Placeholder) {
				_, err = empty.Readdirnames(1)
				if err != io.EOF {
					closeMaybe(empty)
					if err == nil {
						return errors.New("placeholder directory is not empty")
					}
					return err
				}
			}
			closeMaybe(empty)
			if err := checkOriginalMarker(p, r, checkpoint, placeholder); err != nil {
				return err
			}
			if err := moveExchangePhase(p, name, r, "install", checkpoint, placeholder, r.Original, r.Placeholder); err != nil {
				return err
			}
		case "install":
			if err := checkOriginalMarker(p, r, base, placeholder); err != nil {
				return err
			}
			if err := moveExchangePhase(p, name, r, "archive", placeholder, base, r.Original, r.Copy); err != nil {
				return err
			}
		case "archive":
			if err := checkOriginalMarker(p, r, base); err != nil {
				return err
			}
			b, err := openMaybe(p, placeholder)
			if err != nil {
				return err
			}
			a, err := openMaybe(p, archive)
			if err != nil {
				closeMaybe(b)
				return err
			}
			before, after := matches(b, r.Copy) && a == nil, b == nil && matches(a, r.Copy)
			closeMaybe(a)
			closeMaybe(b)
			if before {
				if err = checkTreeMounts(filepath.Join(p.path, placeholder)); err != nil {
					return err
				}
				if err = exchangeOperation("restore:archive", func() error { return p.RenameNoReplace(placeholder, archive) }); err != nil {
					return err
				}
			} else if !after {
				return errors.New("archive state changed")
			}
			if err = exchangeOperation("sync:parent", func() error { return durable.Sync(p.File) }); err != nil {
				return err
			}
			next := "cleanup"
			if r.Remove {
				next = "remove"
			}
			if err = exchangeTransition(p, name, r, next); err != nil {
				return err
			}
		case "remove":
			if err := checkOriginalMarker(p, r, base); err != nil {
				return err
			}
			if err := exchangeOperation("restore:remove", func() error { return restoreRemoveArchive(p, archive, r.Copy) }); err != nil {
				return err
			}
			if err := exchangeTransition(p, name, r, "cleanup"); err != nil {
				return err
			}
		case "cleanup":
			lock, err := openMaybe(p, placeholder)
			if err != nil {
				return err
			}
			if lock != nil {
				closeMaybe(lock)
				return errors.New("placeholder path unexpectedly exists")
			}
			archived, err := openMaybe(p, archive)
			if err != nil {
				return err
			}
			archiveOK := (r.Remove && archived == nil) || (!r.Remove && matches(archived, r.Copy))
			closeMaybe(archived)
			if !archiveOK {
				return errors.New("archive state changed")
			}
			if err := checkOriginalMarker(p, r, base); err != nil {
				return err
			}
			f, err := openMaybe(p, checkpoint)
			if err != nil {
				return err
			}
			if f != nil {
				if err = requireIdentity(f, r.Placeholder); err != nil {
					closeMaybe(f)
					return err
				}
				closeMaybe(f)
				if err = exchangeOperation("restore:cleanup", func() error {
					g, e := openEntry(p.File, checkpoint, syscall.O_RDONLY|syscall.O_DIRECTORY)
					if e != nil {
						return e
					}
					e = requireIdentity(g, r.Placeholder)
					closeMaybe(g)
					if e != nil {
						return e
					}
					return unlinkAt(p.File, checkpoint, unix.AT_REMOVEDIR)
				}); err != nil {
					return err
				}
			}
			if err = exchangeOperation("sync:parent", func() error { return durable.Sync(p.File) }); err != nil {
				return err
			}
			if err = exchangeTransition(p, name, r, "marker"); err != nil {
				return err
			}
		case "marker":
			if err := checkPlaceholderFinalState(p, r, checkpoint, placeholder, archive); err != nil {
				return err
			}
			f, err := openEntry(p.File, base, syscall.O_RDONLY|syscall.O_DIRECTORY)
			if err != nil {
				return err
			}
			if err = requireIdentity(f, r.Original); err == nil {
				var m exchangeMarker
				m, err = newExchangeMarker(r.Target, r.CreatedAt)
				if err == nil {
					err = exchangeOperation("restore:marker", func() error { return removeExchangeMarker(f, m) })
				}
			}
			closeMaybe(f)
			if err != nil {
				return err
			}
			if err = exchangeTransition(p, name, r, "complete"); err != nil {
				return err
			}
		case "complete":
			if err := checkPlaceholderFinalState(p, r, checkpoint, placeholder, archive); err != nil {
				return err
			}
			return finishExchangeRecord(p, name, r)
		default:
			return errors.New("invalid placeholder restore phase")
		}
	}
}
