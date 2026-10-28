//go:build linux || darwin

package session

import (
	"errors"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/filelock"
	"github.com/jiangxin/tiler/internal/image"
)

func TestVersionCleanupSkipsHeldLocksAndRechecksRules(t *testing.T) {
	for _, operation := range []string{"expired", "stale", "removable", "last umount"} {
		t.Run(operation, func(t *testing.T) {
			e, c, action := versionEngine(t)
			if err := e.NewRemovable("dev"); err != nil {
				t.Fatal(err)
			}
			if _, err := e.Append("dev", action); err != nil {
				t.Fatal(err)
			}
			calls := 0
			health := func(string) bool {
				calls++
				if release, err := image.LockLibrary(c.ImagesDir, 0); !errors.Is(err, filelock.ErrHeld) {
					if release != nil {
						release()
					}
					t.Fatalf("cleanup library lock = %v", err)
				}
				if release, err := c.lockSession("dev", 0); !errors.Is(err, ErrLockHeld) {
					if release != nil {
						release()
					}
					t.Fatalf("cleanup session lock = %v", err)
				}
				return true // A view revived after candidate selection.
			}
			run := func() error {
				umount := func(string) error { t.Fatal("recheck allowed teardown"); return nil }
				switch operation {
				case "expired":
					_, err := e.removeExpired("dev", time.Hour, testNow.Add(monthAge))
					return err // The candidate reopened before the mutation.
				case "stale":
					_, err := e.closeStale("dev", testNow, health)
					return err
				case "removable":
					_, err := e.purgeStaleRemovable("dev", health, umount)
					return err
				default:
					_, err := e.closeRemovable("dev", action.Mountpoint, health, umount)
					return err
				}
			}
			for _, library := range []bool{true, false} {
				var release func()
				var err error
				if library {
					release, err = image.LockLibrary(c.ImagesDir, 0)
				} else {
					release, err = c.lockSession("dev", 0)
				}
				if err != nil {
					t.Fatal(err)
				}
				err = run()
				release()
				if err != nil || calls != 0 {
					t.Fatalf("busy cleanup = %v, probes = %d", err, calls)
				}
			}
			if err := run(); err != nil {
				t.Fatal(err)
			}
			requireVersionOwners(t, c, action, 1, 1)
			if e.scope != nil || e.Lock != nil {
				t.Fatal("cleanup changed the shared engine")
			}
		})
	}
}

func TestVersionCleanupRetainsRecoveryPinsAndReleasesPermanentRecords(t *testing.T) {
	for _, removable := range []bool{false, true} {
		t.Run(map[bool]string{false: "stale and expired", true: "implicit removable"}[removable], func(t *testing.T) {
			e, c, action := versionEngine(t)
			if err := e.newRecord("dev", removable); err != nil {
				t.Fatal(err)
			}
			if _, err := e.Append("dev", action); err != nil {
				t.Fatal(err)
			}
			if err := e.Writeback("dev", 1, StatusFailed, ""); err != nil {
				t.Fatal(err)
			}
			before, err := c.LoadVersion("dev")
			if err != nil {
				t.Fatal(err)
			}
			failure := errors.New("teardown failed")
			umount := func(string) error { return failure }
			_, err = e.Cleanup(time.Hour, false, testNow.Add(monthAge), umount)
			if removable && !errors.Is(err, failure) {
				t.Fatalf("failed cleanup = %v", err)
			}
			if !removable && err != nil {
				t.Fatal(err)
			}
			requireVersionOwners(t, c, action, 1, 1)
			umount = func(string) error { return nil }
			if !removable {
				events, err := e.Cleanup(time.Hour, true, testNow.Add(monthAge), umount)
				if err != nil || len(events) != 1 || events[0].Action != CleanupClosed {
					t.Fatalf("autoclose = %v, %v", events, err)
				}
				requireVersionOwners(t, c, action, 1, 1)
			}
			events, err := e.Cleanup(time.Hour, true, testNow.Add(2*monthAge), umount)
			if err != nil || len(events) != 1 || events[0].Action != CleanupRemoved {
				t.Fatalf("permanent cleanup = %v, %v", events, err)
			}
			requireVersionOwners(t, c, action, 0, 0)
			if err := e.New("dev"); err != nil {
				t.Fatal(err)
			}
			if _, err := e.Append("dev", action); err != nil {
				t.Fatal(err)
			}
			if err := c.Apply(&before, nil); !errors.Is(err, ErrReferenceConflict) {
				t.Fatalf("old incarnation removed recreated session: %v", err)
			}
			requireVersionOwners(t, c, action, 1, 1)
		})
	}
}
