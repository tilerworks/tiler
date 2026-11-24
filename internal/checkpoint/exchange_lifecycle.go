//go:build linux || darwin

package checkpoint

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"time"

	"github.com/jiangxin/tiler/internal/config"
	"github.com/jiangxin/tiler/internal/durable"
	"golang.org/x/sys/unix"
)

// exchangeCheckpoint permits interruption tests around durable transitions.
var exchangeCheckpoint = func(string) error { return nil }

var restoreRemoveArchive = removeArchive

func exchangeOperation(name string, f func() error) error {
	if err := exchangeCheckpoint("before:" + name); err != nil {
		return err
	}
	if err := f(); err != nil {
		return err
	}
	return exchangeCheckpoint("after:" + name)
}

func exchangeRecordName(target string) string {
	return "." + filepath.Base(target) + ".tiler-checkpoint-exchange.json"
}

func saveExchange(p *Parent, name string, r *exchangeRecord) error {
	_, old, err := readExchangeRecord(p, name, r.Target)
	if err != nil {
		return err
	}
	return exchangeOperation("record:"+r.Phase, func() error { return writeExchangeRecord(p, name, *r, old) })
}

func exchangeTransition(p *Parent, name string, r *exchangeRecord, phase string) error {
	r.Phase = phase
	return saveExchange(p, name, r)
}

func checkExchangePair(p *Parent, first, second string, firstID, secondID restoreIdentity) error {
	a, err := openMaybe(p, first)
	if err != nil {
		return err
	}
	defer closeMaybe(a)
	b, err := openMaybe(p, second)
	if err != nil {
		return err
	}
	defer closeMaybe(b)
	if !matches(a, firstID) || !matches(b, secondID) {
		return errors.New("exchange directory identity changed; manual inspection required")
	}
	return nil
}

func exchangePair(p *Parent, first, second string, firstID, secondID restoreIdentity) error {
	for _, name := range []string{first, second} {
		if err := checkTreeMounts(filepath.Join(p.path, name)); err != nil {
			return err
		}
	}
	if err := checkExchangePair(p, first, second, firstID, secondID); err != nil {
		return err
	}
	if err := p.Verify(); err != nil {
		return err
	}
	if err := exchangeOperation("exchange", func() error { return p.Exchange(first, second) }); err != nil {
		return err
	}
	if err := checkExchangePair(p, first, second, secondID, firstID); err != nil {
		return err
	}
	return exchangeOperation("sync:parent", func() error { return durable.Sync(p.File) })
}

// validateExchangeCompletion verifies all filesystem identities before replay.
func validateExchangeCompletion(p *Parent, r *exchangeRecord) error {
	active, err := openEntry(p.File, filepath.Base(r.Target), syscall.O_RDONLY|syscall.O_DIRECTORY)
	if err != nil {
		return err
	}
	defer closeMaybe(active)
	want := r.Original
	if r.Operation == "create" {
		want = r.Copy
	}
	if err = requireIdentity(active, want); err != nil {
		return err
	}
	var st unix.Stat_t
	if err = unix.Fstatat(int(active.Fd()), MarkerName, &st, unix.AT_SYMLINK_NOFOLLOW); err == nil {
		return errors.New("active marker unexpectedly exists")
	} else if !errors.Is(err, syscall.ENOENT) {
		return err
	}
	if r.Operation == "restore" {
		if err = unix.Fstatat(int(p.File.Fd()), filepath.Base(r.Target)+".checkpoint", &st, unix.AT_SYMLINK_NOFOLLOW); err == nil {
			return errors.New("checkpoint path unexpectedly exists")
		} else if !errors.Is(err, syscall.ENOENT) {
			return err
		}
	}
	if r.Operation == "create" {
		if err = checkExchangePair(p, filepath.Base(r.Target), filepath.Base(r.Target)+".checkpoint", r.Copy, r.Original); err != nil {
			return err
		}
		orig, e := openEntry(p.File, filepath.Base(r.Target)+".checkpoint", syscall.O_RDONLY|syscall.O_DIRECTORY)
		if e != nil {
			return e
		}
		m, e := readExchangeMarker(orig, r.Target)
		closeMaybe(orig)
		if e != nil {
			return e
		}
		if !m.CreatedAt.Equal(r.CreatedAt) {
			return errors.New("checkpoint marker changed")
		}
	} else if r.HadTarget {
		a, e := openMaybe(p, filepath.Base(r.Archive))
		if e != nil {
			return e
		}
		ok := (r.Remove && a == nil) || (!r.Remove && matches(a, r.Copy))
		closeMaybe(a)
		if !ok {
			return errors.New("archive state changed")
		}
	}
	return nil
}

func exchangeReceiptName(target string) string {
	return "." + filepath.Base(target) + ".tiler-checkpoint-exchange-complete.json"
}

func readExchangeReceipt(p *Parent, target string) (exchangeRecord, os.FileInfo, error) {
	r, info, err := readExchangeRecord(p, exchangeReceiptName(target), target)
	if err == nil && (r.Phase != "complete" || r.Stage != nil) {
		return r, nil, errors.New("invalid completion receipt")
	}
	return r, info, err
}

// finishExchangeRecord publishes a durable receipt before removing the record.
func finishExchangeRecord(p *Parent, name string, r *exchangeRecord) error {
	if err := validateExchangeCompletion(p, r); err != nil {
		return err
	}
	receipt, _, err := readExchangeReceipt(p, r.Target)
	if errors.Is(err, os.ErrNotExist) {
		if err = exchangeOperation("receipt:publish", func() error { return writeExchangeRecord(p, exchangeReceiptName(r.Target), *r, nil) }); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if !reflect.DeepEqual(receipt, *r) {
		return errors.New("completion receipt conflicts with transaction")
	} else if err = durable.Sync(p.File); err != nil {
		return err
	}
	_, old, err := readExchangeRecord(p, name, r.Target)
	if err != nil {
		return err
	}
	return exchangeOperation("record:delete", func() error { return deleteExchangeRecord(p, name, r.Target, old) })
}

// validateExchangeHandoff checks the identities needed by the next operation.
// The user may modify the active tree or manage an old archive after completion.
func validateExchangeHandoff(p *Parent, r *exchangeRecord) error {
	active, err := openMaybe(p, filepath.Base(r.Target))
	if err != nil {
		return err
	}
	defer closeMaybe(active)
	if r.Operation == "create" {
		if active != nil {
			if err = requireIdentity(active, r.Copy); err != nil {
				return err
			}
		}
		orig, err := openEntry(p.File, filepath.Base(r.Target)+".checkpoint", syscall.O_RDONLY|syscall.O_DIRECTORY)
		if err != nil {
			return err
		}
		defer closeMaybe(orig)
		if err = requireIdentity(orig, r.Original); err != nil {
			return err
		}
		m, err := readExchangeMarker(orig, r.Target)
		if err != nil {
			return err
		}
		if !m.CreatedAt.Equal(r.CreatedAt) {
			return errors.New("checkpoint marker changed")
		}
		return nil
	}
	if active == nil {
		return errors.New("restored directory is missing")
	}
	if err = requireIdentity(active, r.Original); err != nil {
		return err
	}
	var st unix.Stat_t
	if err = unix.Fstatat(int(p.File.Fd()), filepath.Base(r.Target)+".checkpoint", &st, unix.AT_SYMLINK_NOFOLLOW); err == nil {
		return errors.New("checkpoint path unexpectedly exists")
	} else if !errors.Is(err, syscall.ENOENT) {
		return err
	}
	return nil
}

// handoffExchangeReceipt verifies both records before dropping an old receipt.
// The new transaction is durable before this is called and has not changed data.
func handoffExchangeReceipt(p *Parent, r *exchangeRecord) error {
	old, info, err := readExchangeReceipt(p, r.Target)
	if errors.Is(err, os.ErrNotExist) {
		// Make a prior receipt unlink durable before advancing the new record.
		return durable.Sync(p.File)
	}
	if err != nil {
		return err
	}
	if old.Operation == r.Operation {
		if r.Phase == "complete" && reflect.DeepEqual(old, *r) {
			return nil
		}
		return errors.New("completion receipt conflicts with transaction")
	}
	if err = validateExchangeHandoff(p, &old); err != nil {
		return fmt.Errorf("completion receipt state: %w", err)
	}
	if old.Operation == "create" {
		if r.Phase != "prepared" || r.Original != old.Original || !r.CreatedAt.Equal(old.CreatedAt) || (r.HadTarget && r.Copy != old.Copy) {
			return errors.New("restore does not match completion receipt")
		}
	} else if r.Phase != "intent" || r.Original != old.Original {
		return errors.New("create does not match completion receipt")
	}
	return exchangeOperation("receipt:delete", func() error { return deleteExchangeRecord(p, exchangeReceiptName(r.Target), r.Target, info) })
}

func exchangeStateError(r *exchangeRecord, err error) error {
	if r.Operation == "create" && (r.Phase == "exchange" || r.Phase == "marker" || r.Phase == "complete") {
		err = fmt.Errorf("%w (the active path may point to the task copy)", err)
	}
	if r.MarkerRemoved {
		err = fmt.Errorf("%w (existing %s was removed)", err, MarkerName)
	}
	return fmt.Errorf("exchange transaction for %s requires retry or manual inspection (temporary %s, checkpoint %s, archive %s): %w", r.Target, r.Temporary, r.Target+".checkpoint", r.Archive, err)
}

// runExchangeStage creates and records a sibling inode before linking it into
// the original directory. A file found before its identity was persisted is
// intentionally left for manual inspection.
func runExchangeStage(p *Parent, name string, r *exchangeRecord, dir *os.File, purpose string, data []byte) error {
	if r.Stage == nil {
		stageName := "." + filepath.Base(r.Target) + ".tiler-checkpoint-stage-" + purpose
		r.Stage = &exchangeStage{Path: filepath.Join(p.path, stageName), Purpose: purpose}
		if err := saveExchange(p, name, r); err != nil {
			return err
		}
	}
	if r.Stage.Purpose != purpose {
		return errors.New("unexpected exchange stage")
	}
	stageName := filepath.Base(r.Stage.Path)
	if r.Stage.Identity.Inode == 0 {
		if _, err := os.Lstat(r.Stage.Path); err == nil {
			return fmt.Errorf("unrecorded exchange stage %s requires manual inspection", r.Stage.Path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		var id restoreIdentity
		err := exchangeOperation("stage:create", func() error { var e error; id, e = createExchangeStage(p, stageName, data); return e })
		if err != nil {
			return err
		}
		r.Stage.Identity = id
		if err = saveExchange(p, name, r); err != nil {
			return err
		}
	}
	dest := MarkerName
	if purpose == "probe" {
		dest = stageName
	}
	if !r.Stage.Cleanup {
		if err := exchangeOperation("stage:link", func() error { return linkExchangeStage(p, stageName, dir, dest, r.Stage.Identity) }); err != nil {
			return err
		}
		r.Stage.Cleanup = true
		if err := saveExchange(p, name, r); err != nil {
			return err
		}
	}
	if purpose == "probe" {
		if err := exchangeOperation("stage:remove-dest", func() error { return removeExchangeStage(dir, dest, r.Stage.Identity) }); err != nil {
			return err
		}
	} else {
		f, err := openExchangeStage(dir, dest, r.Stage.Identity)
		if err != nil {
			return err
		}
		_ = f.Close()
	}
	if err := exchangeOperation("stage:remove-source", func() error { return removeExchangeStage(p.File, stageName, r.Stage.Identity) }); err != nil {
		return err
	}
	r.Stage = nil
	return saveExchange(p, name, r)
}

func moveExchangePhase(p *Parent, name string, r *exchangeRecord, phase string, first, second string, beforeA, beforeB restoreIdentity) error {
	a, err := openMaybe(p, first)
	if err != nil {
		return err
	}
	b, err := openMaybe(p, second)
	if err != nil {
		closeMaybe(a)
		return err
	}
	before := matches(a, beforeA) && matches(b, beforeB)
	after := matches(a, beforeB) && matches(b, beforeA)
	closeMaybe(a)
	closeMaybe(b)
	switch {
	case before:
		if r.Operation == "restore" {
			if err := requireArchiveAbsent(p, filepath.Base(r.Archive)); err != nil {
				return err
			}
		}
		if err := exchangePair(p, first, second, beforeA, beforeB); err != nil {
			return err
		}
	case after:
		if err := durable.Sync(p.File); err != nil {
			return err
		}
	default:
		return errors.New("exchange state changed; manual inspection required")
	}
	return exchangeTransition(p, name, r, phase)
}

// removeRecordedOldMarker records the file identity before deletion so a
// retry can recognize deletion even when the process stops after unlink.
func removeRecordedOldMarker(p *Parent, name string, r *exchangeRecord, dir *os.File) error {
	fd, err := unix.Openat(int(dir.Fd()), MarkerName, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if errors.Is(err, syscall.ENOENT) {
		if r.OldMarker.Inode != 0 && !r.MarkerRemoved {
			r.MarkerRemoved = true
			if err = durable.Sync(dir); err != nil {
				return err
			}
			return saveExchange(p, name, r)
		}
		return nil
	}
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), MarkerName)
	defer closeMaybe(f)
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("existing checkpoint marker is not a regular file")
	}
	id, err := identity(f)
	if err != nil {
		return err
	}
	if r.OldMarker.Inode == 0 {
		r.OldMarker = id
		if err = saveExchange(p, name, r); err != nil {
			return err
		}
	} else if r.OldMarker != id || r.MarkerRemoved {
		return errors.New("existing marker identity changed")
	}
	var st syscall.Stat_t
	if err = syscall.Fstat(fd, &st); err != nil {
		return err
	}
	if err = verifyNamed(dir, MarkerName, &st); err != nil {
		return err
	}
	if err = exchangeOperation("old-marker:unlink", func() error { return unlinkAt(dir, MarkerName, 0) }); err != nil {
		return err
	}
	r.MarkerRemoved = true
	if err = durable.Sync(dir); err != nil {
		return err
	}
	return saveExchange(p, name, r)
}

func runExchangeCreate(p *Parent, name string, r *exchangeRecord) error {
	base := filepath.Base(r.Target)
	tmp := filepath.Base(r.Temporary)
	checkpoint := base + ".checkpoint"
	for {
		if err := p.Verify(); err != nil {
			return err
		}
		switch r.Phase {
		case "intent":
			src, err := openEntry(p.File, base, syscall.O_RDONLY|syscall.O_DIRECTORY)
			if err != nil {
				return err
			}
			if err = requireIdentity(src, r.Original); err != nil {
				closeMaybe(src)
				return err
			}
			err = runExchangeStage(p, name, r, src, "probe", []byte("tiler checkpoint probe"))
			if err == nil {
				err = rejectRecognizedMarker(src, r.Target)
			}
			if err == nil {
				err = removeRecordedOldMarker(p, name, r, src)
			}
			closeMaybe(src)
			if err != nil {
				return err
			}
			if _, err = os.Lstat(r.Temporary); err == nil {
				return fmt.Errorf("unrecorded temporary directory %s requires manual inspection", r.Temporary)
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err = exchangeOperation("mkdir:temporary", func() error { return unix.Mkdirat(int(p.File.Fd()), tmp, 0700) }); err != nil {
				return err
			}
			dir, err := openEntry(p.File, tmp, syscall.O_RDONLY|syscall.O_DIRECTORY)
			if err != nil {
				return err
			}
			r.Copy, err = identity(dir)
			closeMaybe(dir)
			if err != nil {
				return err
			}
			if err = exchangeTransition(p, name, r, "copy"); err != nil {
				return err
			}
			if err = copyExchangeTree(p, r, base, tmp); err != nil {
				return err
			}
			if err = exchangeTransition(p, name, r, "publish"); err != nil {
				return err
			}
		case "copy":
			// A partial copy cannot be resumed safely. Remove only the recorded
			// temporary directory and start copying again.
			if err := removeTemporaryCopy(p, tmp, r.Copy); err != nil {
				return err
			}
			r.Copy = restoreIdentity{}
			if err := exchangeTransition(p, name, r, "intent"); err != nil {
				return err
			}
		case "publish":
			src, err := openMaybe(p, tmp)
			if err != nil {
				return err
			}
			dst, err := openMaybe(p, checkpoint)
			if err != nil {
				closeMaybe(src)
				return err
			}
			canMove := matches(src, r.Copy) && dst == nil
			moved := src == nil && matches(dst, r.Copy)
			closeMaybe(src)
			closeMaybe(dst)
			if canMove {
				if err = exchangeOperation("publish", func() error { return p.RenameNoReplace(tmp, checkpoint) }); err != nil {
					return err
				}
			} else if !moved {
				return errors.New("copy publication state changed")
			}
			if err = exchangeOperation("sync:parent", func() error { return durable.Sync(p.File) }); err != nil {
				return err
			}
			if err = exchangeTransition(p, name, r, "exchange"); err != nil {
				return err
			}
		case "exchange":
			if err := moveExchangePhase(p, name, r, "marker", base, checkpoint, r.Original, r.Copy); err != nil {
				return err
			}
		case "marker":
			if err := checkExchangePair(p, base, checkpoint, r.Copy, r.Original); err != nil {
				return err
			}
			orig, err := openEntry(p.File, checkpoint, syscall.O_RDONLY|syscall.O_DIRECTORY)
			if err != nil {
				return err
			}
			marker, err := newExchangeMarker(r.Target, r.CreatedAt)
			if err == nil {
				var data []byte
				data, err = marshalExchangeMarker(marker)
				if err == nil {
					existing, readErr := readExchangeMarker(orig, r.Target)
					if readErr == nil && existing.CreatedAt.Equal(r.CreatedAt) && r.Stage == nil {
						err = durable.Sync(orig)
					} else {
						err = runExchangeStage(p, name, r, orig, "marker", data)
					}
				}
			}
			closeMaybe(orig)
			if err != nil {
				return err
			}
			if err = exchangeTransition(p, name, r, "complete"); err != nil {
				return err
			}
		case "complete":
			if err := checkExchangePair(p, base, checkpoint, r.Copy, r.Original); err != nil {
				return err
			}
			orig, err := openEntry(p.File, checkpoint, syscall.O_RDONLY|syscall.O_DIRECTORY)
			if err != nil {
				return err
			}
			m, err := readExchangeMarker(orig, r.Target)
			closeMaybe(orig)
			if err != nil {
				return err
			}
			if !m.CreatedAt.Equal(r.CreatedAt) {
				return errors.New("checkpoint marker changed")
			}
			return finishExchangeRecord(p, name, r)
		default:
			return errors.New("invalid create phase")
		}
	}
}

func copyExchangeTree(p *Parent, r *exchangeRecord, base, tmp string) error {
	src, err := openEntry(p.File, base, syscall.O_RDONLY|syscall.O_DIRECTORY)
	if err != nil {
		return err
	}
	defer closeMaybe(src)
	dst, err := openEntry(p.File, tmp, syscall.O_RDONLY|syscall.O_DIRECTORY)
	if err != nil {
		return err
	}
	defer closeMaybe(dst)
	if err = requireIdentity(src, r.Original); err != nil {
		return err
	}
	if err = requireIdentity(dst, r.Copy); err != nil {
		return err
	}
	if err = exchangeOperation("copy", func() error { return CopyTree(src, dst) }); err != nil {
		return err
	}
	if err = exchangeOperation("sync:copy", func() error { return durable.Sync(dst) }); err != nil {
		return err
	}
	return checkExchangePair(p, base, tmp, r.Original, r.Copy)
}

func runExchangeRestore(p *Parent, name string, r *exchangeRecord) error {
	if r.HadTarget {
		return runPlaceholderRestore(p, name, r)
	}
	base := filepath.Base(r.Target)
	checkpoint := base + ".checkpoint"
	for {
		if err := p.Verify(); err != nil {
			return err
		}
		switch r.Phase {
		case "prepared":
			if err := exchangeTransition(p, name, r, "exchange"); err != nil {
				return err
			}
		case "exchange":
			a, err := openMaybe(p, base)
			if err != nil {
				return err
			}
			b, err := openMaybe(p, checkpoint)
			if err != nil {
				closeMaybe(a)
				return err
			}
			before := a == nil && matches(b, r.Original)
			after := matches(a, r.Original) && b == nil
			closeMaybe(a)
			closeMaybe(b)
			if before {
				if err = exchangeOperation("restore:install", func() error { return p.RenameNoReplace(checkpoint, base) }); err != nil {
					return err
				}
			} else if !after {
				return errors.New("direct restore state changed")
			}
			if err = exchangeOperation("sync:parent", func() error { return durable.Sync(p.File) }); err != nil {
				return err
			}
			if err = exchangeTransition(p, name, r, "marker"); err != nil {
				return err
			}
		case "marker":
			a, err := openEntry(p.File, base, syscall.O_RDONLY|syscall.O_DIRECTORY)
			if err != nil {
				return err
			}
			if err = requireIdentity(a, r.Original); err == nil {
				var marker exchangeMarker
				marker, err = newExchangeMarker(r.Target, r.CreatedAt)
				if err == nil {
					err = exchangeOperation("restore:marker", func() error { return removeExchangeMarker(a, marker) })
				}
			}
			closeMaybe(a)
			if err != nil {
				return err
			}
			if err = exchangeTransition(p, name, r, "complete"); err != nil {
				return err
			}
		case "complete":
			return finishExchangeRecord(p, name, r)
		default:
			return errors.New("invalid restore phase")
		}
	}
}

// exchangeContext resolves and locks the target before any transaction work.
func exchangeContext(path, home string, cfg config.Config) (string, *Parent, *os.File, error) {
	target, err := Resolve(path)
	if err != nil {
		return "", nil, nil, err
	}
	mounts, err := checkpointMounts()
	if err != nil {
		return "", nil, nil, err
	}
	if err = CheckPath(target, home, cfg, mounts); err != nil {
		return "", nil, nil, err
	}
	if err = CheckPath(target+".checkpoint", home, cfg, mounts); err != nil {
		return "", nil, nil, err
	}
	p, err := OpenParent(target)
	if err != nil {
		return "", nil, nil, err
	}
	lock, err := p.Lock("." + filepath.Base(target) + ".tiler-checkpoint.lock")
	if err != nil {
		_ = p.Close()
		return "", nil, nil, err
	}
	if err = p.Verify(); err != nil {
		_ = lock.Close()
		_ = p.Close()
		return "", nil, nil, err
	}
	for _, root := range []string{target, target + ".checkpoint"} {
		if err = checkTreeMounts(root); err != nil {
			_ = lock.Close()
			_ = p.Close()
			return "", nil, nil, err
		}
	}
	return target, p, lock, nil
}

func rejectLegacyTransactions(p *Parent, target string) error {
	base := "." + filepath.Base(target)
	for _, suffix := range []string{".tiler-checkpoint-create", ".tiler-checkpoint-create.json", ".tiler-checkpoint-create.json.new", ".tiler-checkpoint-restore.json"} {
		path := filepath.Join(p.path, base+suffix)
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("legacy transaction object %s requires manual inspection", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// Create creates a task copy and atomically swaps it with the original.
func Create(path, home string, cfg config.Config) (string, error) {
	pathOut, _, err := CreateWithStatus(path, home, cfg)
	return pathOut, err
}

// CreateWithStatus reports whether an existing valid checkpoint was reused.
func CreateWithStatus(path, home string, cfg config.Config) (string, bool, error) {
	existing := false
	result, err := createWithStatus(path, home, cfg, &existing)
	return result, existing, err
}

func createWithStatus(path, home string, cfg config.Config, existing *bool) (string, error) {
	target, p, lock, err := exchangeContext(path, home, cfg)
	if err != nil {
		return "", err
	}
	defer func() { _ = lock.Close(); _ = p.Close() }()
	if err = rejectLegacyTransactions(p, target); err != nil {
		return "", err
	}
	if err = rejectCheckpointSource(target); err != nil {
		return "", err
	}
	name := exchangeRecordName(target)
	r, _, err := readExchangeRecord(p, name, target)
	if errors.Is(err, os.ErrNotExist) {
		receipt, _, recErr := readExchangeReceipt(p, target)
		if recErr == nil {
			validate := validateExchangeHandoff
			if receipt.Operation == "create" {
				validate = validateExchangeCompletion
			}
			if e := validate(p, &receipt); e != nil {
				return "", fmt.Errorf("completion receipt state: %w", e)
			}
			if receipt.Operation == "create" {
				if e := durable.Sync(p.File); e != nil {
					return "", e
				}
				*existing = true
				return target + ".checkpoint", nil
			}
		} else if !errors.Is(recErr, os.ErrNotExist) {
			return "", recErr
		}
		if _, err = os.Lstat(target + ".checkpoint"); err == nil {
			f, openErr := openEntry(p.File, filepath.Base(target)+".checkpoint", syscall.O_RDONLY|syscall.O_DIRECTORY)
			if openErr != nil {
				return "", openErr
			}
			_, markerErr := readExchangeMarker(f, target)
			closeMaybe(f)
			if markerErr != nil {
				return "", fmt.Errorf("invalid existing checkpoint: %w", markerErr)
			}
			*existing = true
			return target + ".checkpoint", nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		src, e := openEntry(p.File, filepath.Base(target), syscall.O_RDONLY|syscall.O_DIRECTORY)
		if e != nil {
			return "", e
		}
		original, e := identity(src)
		if e == nil {
			e = rejectRecognizedMarker(src, target)
		}
		closeMaybe(src)
		if e != nil {
			return "", e
		}
		if e = checkTreeMounts(target); e != nil {
			return "", e
		}
		r = exchangeRecord{Version: 3, Protocol: "exchange", Operation: "create", Target: target,
			Temporary: filepath.Join(p.path, "."+filepath.Base(target)+".tiler-checkpoint-create-v2"),
			HadTarget: true, Original: original, CreatedAt: time.Now().UTC(), Phase: "intent"}
		if err = exchangeOperation("record:"+r.Phase, func() error { return writeExchangeRecord(p, name, r, nil) }); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	if r.Operation != "create" {
		return "", fmt.Errorf("unfinished %s transaction requires retry with restore", r.Operation)
	}
	if err = handoffExchangeReceipt(p, &r); err != nil {
		return "", err
	}
	if err = runExchangeCreate(p, name, &r); err != nil {
		// The unlink can succeed even if its following checkpoint or sync
		// fails. The recorded intent lets us report that side effect.
		if !r.MarkerRemoved && r.OldMarker.Inode != 0 && r.Phase == "intent" {
			if dir, openErr := openEntry(p.File, filepath.Base(target), syscall.O_RDONLY|syscall.O_DIRECTORY); openErr == nil {
				if requireIdentity(dir, r.Original) == nil {
					var st unix.Stat_t
					if errors.Is(unix.Fstatat(int(dir.Fd()), MarkerName, &st, unix.AT_SYMLINK_NOFOLLOW), syscall.ENOENT) {
						r.MarkerRemoved = true
					}
				}
				closeMaybe(dir)
			}
		}
		return "", exchangeStateError(&r, err)
	}
	return target + ".checkpoint", nil
}

func rejectRecognizedMarker(dir *os.File, _ string) error {
	fd, err := unix.Openat(int(dir.Fd()), MarkerName, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if errors.Is(err, syscall.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), MarkerName)
	defer closeMaybe(f)
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("existing checkpoint marker is not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil {
		return err
	}
	if len(data) > 4096 {
		return errors.New("existing checkpoint marker exceeds size limit")
	}
	// Preserve versioned objects even when their target or protocol is unknown.
	var object map[string]json.RawMessage
	if json.Unmarshal(data, &object) == nil {
		if _, ok := object["version"]; ok {
			return errors.New("existing versioned checkpoint marker requires manual inspection")
		}
		if _, ok := object["protocol"]; ok {
			return errors.New("existing protocol marker requires manual inspection")
		}
	}
	return nil
}

// Restore atomically returns the original directory and archives the task copy.
func Restore(path, home string, cfg config.Config, remove bool) (string, error) {
	result, _, err := RestoreWithStatus(path, home, cfg, remove)
	return result, err
}

// RestoreWithStatus reports whether there was no checkpoint to restore.
func RestoreWithStatus(path, home string, cfg config.Config, remove bool) (string, bool, error) {
	missing := false
	result, err := restoreWithStatus(path, home, cfg, remove, &missing)
	return result, missing, err
}

func restoreWithStatus(path, home string, cfg config.Config, remove bool, missing *bool) (string, error) {
	target, p, lock, err := exchangeContext(path, home, cfg)
	if err != nil {
		return "", err
	}
	defer func() { _ = lock.Close(); _ = p.Close() }()
	if err = rejectLegacyTransactions(p, target); err != nil {
		return "", err
	}
	name := exchangeRecordName(target)
	r, _, err := readExchangeRecord(p, name, target)
	if errors.Is(err, os.ErrNotExist) {
		receipt, _, recErr := readExchangeReceipt(p, target)
		if recErr == nil {
			validate := validateExchangeHandoff
			if receipt.Operation == "restore" {
				validate = validateExchangeCompletion
			}
			if e := validate(p, &receipt); e != nil {
				return "", fmt.Errorf("completion receipt state: %w", e)
			}
			if receipt.Operation == "restore" {
				if e := durable.Sync(p.File); e != nil {
					return "", e
				}
				*missing = true
				return receipt.Archive, nil
			}
		} else if !errors.Is(recErr, os.ErrNotExist) {
			return "", recErr
		}
		r, err = prepareExchangeRestore(p, target, remove)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				if _, statErr := os.Lstat(target + ".checkpoint"); errors.Is(statErr, os.ErrNotExist) {
					*missing = true
					return "", nil
				}
			}
			return "", err
		}
		if r.Archive != "" {
			mounts, e := checkpointMounts()
			if e != nil {
				return "", e
			}
			if e = CheckPath(r.Archive, home, cfg, mounts); e != nil {
				return "", e
			}
		}
		if err = exchangeOperation("record:"+r.Phase, func() error { return writeExchangeRecord(p, name, r, nil) }); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	if r.Operation != "restore" {
		return "", errors.New("unfinished create transaction requires retry with create")
	}
	if err = handoffExchangeReceipt(p, &r); err != nil {
		return "", err
	}
	if err = runExchangeRestore(p, name, &r); err != nil {
		return "", exchangeStateError(&r, err)
	}
	return r.Archive, nil
}

func prepareExchangeRestore(p *Parent, target string, remove bool) (exchangeRecord, error) {
	r := exchangeRecord{Version: 3, Protocol: "placeholder", Operation: "restore", Target: target, Remove: remove, Phase: "prepared"}
	base := filepath.Base(target)
	if err := requireArchiveAbsent(p, base+".checkpoint.lock"); err != nil {
		return r, fmt.Errorf("placeholder already exists: %w", err)
	}
	b, err := openEntry(p.File, base+".checkpoint", syscall.O_RDONLY|syscall.O_DIRECTORY)
	if err != nil {
		return r, err
	}
	defer closeMaybe(b)
	if err = checkTreeMounts(target + ".checkpoint"); err != nil {
		return r, err
	}
	m, err := readExchangeMarker(b, target)
	if err != nil {
		return r, fmt.Errorf("invalid or legacy checkpoint marker; manual inspection required: %w", err)
	}
	r.Original, err = identity(b)
	if err != nil {
		return r, err
	}
	r.CreatedAt = m.CreatedAt
	a, err := openMaybe(p, base)
	if err != nil {
		return r, err
	}
	if a == nil {
		return r, nil
	}
	defer closeMaybe(a)
	if err = checkTreeMounts(target); err != nil {
		return r, err
	}
	r.HadTarget = true
	r.Copy, err = identity(a)
	if err != nil {
		return r, err
	}
	r.Archive, err = archiveName(target, Marker{Version: markerVersion, Target: target, CreatedAt: m.CreatedAt})
	if err != nil {
		return r, err
	}
	return r, requireArchiveAbsent(p, filepath.Base(r.Archive))
}

func requireArchiveAbsent(p *Parent, name string) error {
	var st unix.Stat_t
	err := unix.Fstatat(int(p.File.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW)
	if err == nil {
		return os.ErrExist
	}
	if errors.Is(err, syscall.ENOENT) {
		return nil
	}
	return err
}
