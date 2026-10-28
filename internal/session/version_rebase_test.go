//go:build linux || darwin

package session

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestVersionRebaseRetainsServedAndReplayableBindings(t *testing.T) {
	e, c, action := versionEngine(t)
	action.ServedID, action.ServedTip = "", ""
	if err := e.New("dev"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.RunMount("dev", action, func(a Action) (Result, error) {
		return Result{Mountpoint: a.Mountpoint}, nil
	}); err != nil {
		t.Fatal(err)
	}
	before, err := c.LoadVersion("dev")
	if err != nil {
		t.Fatal(err)
	}
	bound := action
	bound.ServedTip = action.TipID
	for _, tip := range []string{strings.Repeat("e", 64), strings.Repeat("f", 64)} {
		if err := e.RebaseMountedStack(action.Mountpoint, action.Name, action.FullID, tip); err != nil {
			t.Fatal(err)
		}
		bound.TipID = tip
		requireVersionOwners(t, c, bound, 1, 1)
		rec, err := c.LoadVersion("dev")
		if err != nil || rec.Actions[0].TipID != tip || rec.Actions[0].ServedTip != action.TipID ||
			rec.Actions[0].ServedID != action.FullID || rec.InstanceID != before.InstanceID {
			t.Fatalf("rebased record = %#v, %v", rec, err)
		}
	}
	intermediate, err := c.References(ReferenceTarget{action.Name, strings.Repeat("e", 64)})
	if err != nil || len(intermediate[0].Owners) != 0 {
		t.Fatalf("intermediate build remains pinned: %#v, %v", intermediate, err)
	}
	if err := c.Apply(&before, nil); !errors.Is(err, ErrReferenceConflict) {
		t.Fatalf("stale pre-build removal = %v", err)
	}
	if err := e.Remove("dev"); err == nil {
		t.Fatal("active record removed")
	}
	if err := e.ReleaseMount(action.Mountpoint); err != nil {
		t.Fatal(err)
	}
	requireVersionOwners(t, c, bound, 1, 1)
	if _, err := e.Close("dev", func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	requireVersionOwners(t, c, bound, 1, 1)
	// A closed binding must stay pinned to its desired and served tips.
	if err := e.RebaseMountedStack(action.Mountpoint, action.Name, action.FullID, action.TipID); err != nil {
		t.Fatal(err)
	}
	requireVersionOwners(t, c, bound, 1, 1)
	if err := e.Open("dev", func(a Action) (Result, error) {
		if a.TipID != bound.TipID || a.ServedTip != bound.ServedTip {
			t.Fatalf("remount lost build bindings: %#v", a)
		}
		return Result{Mountpoint: a.Mountpoint}, nil
	}); err != nil {
		t.Fatal(err)
	}
	requireVersionOwners(t, c, bound, 1, 0)
	if _, err := e.Close("dev", func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := e.Remove("dev"); err != nil {
		t.Fatal(err)
	}
	requireVersionOwners(t, c, bound, 0, 0)
	if _, err := c.LoadVersion("dev"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removed source = %v", err)
	}
}

func TestVersionDiscardReleasesOnlyPermanentlyDiscardedBindings(t *testing.T) {
	for _, test := range []struct {
		name           string
		failed, reused bool
		removed        bool
	}{
		{name: "failed implicit mount", failed: true, removed: true},
		{name: "adopted implicit mount", reused: true, removed: true},
		{name: "owned live mount"},
	} {
		t.Run(test.name, func(t *testing.T) {
			e, c, action := versionEngine(t)
			if err := e.New("dev"); err != nil {
				t.Fatal(err)
			}
			failure := errors.New("mount failed")
			_, err := e.RunMount("dev", action, func(a Action) (Result, error) {
				if test.failed {
					return Result{}, failure
				}
				return Result{Mountpoint: a.Mountpoint, Reused: test.reused}, nil
			})
			if (test.failed && !errors.Is(err, failure)) || (!test.failed && err != nil) {
				t.Fatal(err)
			}
			requireVersionOwners(t, c, action, 1, 1)
			removed, err := e.Discard("dev", test.reused)
			if err != nil || removed != test.removed {
				t.Fatalf("discard = %v, %v", removed, err)
			}
			owners := 1
			if removed {
				owners = 0
			}
			requireVersionOwners(t, c, action, owners, owners)
		})
	}
}

func TestVersionRebaseRejectsInvalidIdentityWithoutChangingBindings(t *testing.T) {
	e, c, action := versionEngine(t)
	if err := e.New("dev"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Append("dev", action); err != nil {
		t.Fatal(err)
	}
	before, err := c.LoadVersion("dev")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.RebaseMountedStack(action.Mountpoint, action.Name, action.FullID, "abcd"); err == nil {
		t.Fatal("incomplete rebase identity accepted")
	}
	after, err := c.LoadVersion("dev")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("rejected rebase changed source = %#v, %v", after, err)
	}
	requireVersionOwners(t, c, action, 1, 1)
}
