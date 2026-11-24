//go:build linux || darwin

package checkpoint

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"

	"github.com/jiangxin/tiler/internal/durable"
	"golang.org/x/sys/unix"
)

func readExchangeRecord(p *Parent, name, target string) (exchangeRecord, os.FileInfo, error) {
	var empty exchangeRecord
	if err := safeName(name); err != nil {
		return empty, nil, err
	}
	fd, err := unix.Openat(int(p.File.Fd()), name, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return empty, nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return empty, nil, err
	}
	if !info.Mode().IsRegular() {
		return empty, nil, errors.New("exchange record is not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil {
		return empty, nil, err
	}
	if len(data) > 4096 {
		return empty, nil, errors.New("exchange record exceeds size limit")
	}
	rec, err := parseExchangeRecord(data, target)
	return rec, info, err
}

// writeExchangeRecord publishes a new record, or updates the verified record.
// The caller must hold the sibling lock throughout the transaction.
func writeExchangeRecord(p *Parent, name string, rec exchangeRecord, previous os.FileInfo) error {
	if err := safeName(name); err != nil {
		return err
	}
	if err := rec.validate(rec.Target); err != nil {
		return err
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if err := p.Verify(); err != nil {
		return err
	}
	var suffix [16]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return err
	}
	temp := ".tiler-checkpoint-exchange-record-" + hex.EncodeToString(suffix[:])
	fd, err := unix.Openat(int(p.File.Fd()), temp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), temp)
	defer closeMaybe(f)
	var st syscall.Stat_t
	if err = syscall.Fstat(fd, &st); err != nil {
		return err
	}
	defer func() {
		if verifyNamed(p.File, temp, &st) == nil {
			_ = unlinkAt(p.File, temp, 0)
		}
	}()
	if err = exchangeRecordOperation("record:write", func() error { _, e := f.Write(data); return e }); err != nil {
		return err
	}
	if err = exchangeRecordOperation("record:sync-file", func() error { return durable.Sync(f) }); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = p.Verify(); err != nil {
		return err
	}
	if err = verifyNamed(p.File, temp, &st); err != nil {
		return err
	}
	if previous == nil {
		if err = exchangeRecordOperation("record:publish", func() error { return p.RenameNoReplace(temp, name) }); err != nil {
			return err
		}
	} else {
		_, current, e := readExchangeRecord(p, name, rec.Target)
		if e != nil {
			return e
		}
		if !os.SameFile(previous, current) {
			return errors.New("exchange record changed")
		}
		if err = exchangeRecordOperation("record:replace", func() error { return unix.Renameat(int(p.File.Fd()), temp, int(p.File.Fd()), name) }); err != nil {
			return fmt.Errorf("replace exchange record: %w", err)
		}
	}
	return exchangeRecordOperation("record:sync-parent", func() error { return durable.Sync(p.File) })
}

func deleteExchangeRecord(p *Parent, name, target string, previous os.FileInfo) error {
	_, current, err := readExchangeRecord(p, name, target)
	if err != nil {
		return err
	}
	if previous == nil || !os.SameFile(previous, current) {
		return errors.New("exchange record changed")
	}
	if err = p.Verify(); err != nil {
		return err
	}
	if err = exchangeRecordOperation("record:unlink", func() error { return unlinkAt(p.File, name, 0) }); err != nil {
		return err
	}
	return exchangeRecordOperation("record:sync-parent", func() error { return durable.Sync(p.File) })
}

// exchangeRecordOperation permits tests to inject interruptions at durable boundaries.
var exchangeRecordOperation = func(_ string, operation func() error) error { return operation() }
