//go:build linux

package checkpoint

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func exchangeTestRecord(target string) exchangeRecord {
	return exchangeRecord{Version: 3, Protocol: "exchange", Operation: "create", Target: target, Temporary: filepath.Join(filepath.Dir(target), "."+filepath.Base(target)+".tiler-checkpoint-temp"), HadTarget: true, Original: restoreIdentity{Device: 1, Inode: 2}, CreatedAt: time.Unix(1, 0).UTC(), Phase: "intent"}
}

func TestExchangeRecordDurableLifecycle(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "active")
	p, err := OpenParent(target)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	r := exchangeTestRecord(target)
	if err := writeExchangeRecord(p, "record", r, nil); err != nil {
		t.Fatal(err)
	}
	if err := writeExchangeRecord(p, "record", r, nil); !errors.Is(err, os.ErrExist) {
		t.Fatalf("overwrite: %v", err)
	}
	got, old, err := readExchangeRecord(p, "record", target)
	if err != nil || got != r {
		t.Fatalf("read = %+v, %v", got, err)
	}
	r.Copy = restoreIdentity{Device: 1, Inode: 3}
	r.Phase = "copy"
	if err := writeExchangeRecord(p, "record", r, old); err != nil {
		t.Fatal(err)
	}
	_, current, err := readExchangeRecord(p, "record", target)
	if err != nil {
		t.Fatal(err)
	}
	if err := deleteExchangeRecord(p, "record", target, old); err == nil {
		t.Fatal("deleted replaced record")
	}
	if err := deleteExchangeRecord(p, "record", target, current); err != nil {
		t.Fatal(err)
	}
}

func TestExchangeRecordRejectsUntrustedObjects(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "active")
	p, err := OpenParent(target)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	if err := os.WriteFile(filepath.Join(root, "record"), []byte(`{"version":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readExchangeRecord(p, "record", target); err == nil {
		t.Fatal("accepted old record")
	}
	if err := os.Symlink("record", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readExchangeRecord(p, "link", target); err == nil {
		t.Fatal("followed record link")
	}
	if err := writeExchangeRecord(p, "record", exchangeTestRecord(target), nil); !errors.Is(err, os.ErrExist) {
		t.Fatalf("replaced untrusted record: %v", err)
	}
}

func TestExchangeRecordWriteFailures(t *testing.T) {
	for _, step := range []string{"record:write", "record:sync-file", "record:publish", "record:sync-parent"} {
		t.Run(step, func(t *testing.T) {
			root := t.TempDir()
			target := filepath.Join(root, "active")
			p, err := OpenParent(target)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = p.Close() }()
			sentinel := errors.New("injected")
			previous := exchangeRecordOperation
			exchangeRecordOperation = func(name string, op func() error) error {
				if name == step {
					return sentinel
				}
				return op()
			}
			defer func() { exchangeRecordOperation = previous }()
			if err := writeExchangeRecord(p, "record", exchangeTestRecord(target), nil); !errors.Is(err, sentinel) {
				t.Fatalf("error = %v", err)
			}
			if step == "record:sync-parent" {
				if _, _, err := readExchangeRecord(p, "record", target); err != nil {
					t.Fatalf("published record: %v", err)
				}
			} else if _, err := os.Lstat(filepath.Join(root, "record")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unexpected record: %v", err)
			}
		})
	}
}
