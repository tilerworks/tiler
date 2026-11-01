package session

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/mountinfo"
)

func TestQueryActivityIntersectsFiltersAndRetainsCompleteViews(t *testing.T) {
	e, root := lookupEngine(t, nil, nil)
	e.SessionsDir = root
	point := filepath.Join(t.TempDir(), "view")
	seedMounted(t, e, "missing", point, point+"-other")
	seedMounted(t, e, "closed", point)
	seedMounted(t, e, "residual", point)
	for _, name := range []string{"closed", "residual"} {
		rec, err := e.Load(name)
		if err != nil {
			t.Fatal(err)
		}
		rec.State, rec.ClosedAt = StateClosed, testNow
		rec.Actions[0].Status = StatusUnmounted
		if err := e.Save(rec); err != nil {
			t.Fatal(err)
		}
	}
	table := []mountinfo.Mount{
		{Point: filepath.Join(root, "residual", "runtime", "m1", "lower-0"), FSType: "fuse"},
		overlayEntry(point+"-orphan", filepath.Join(root, "orphan", "runtime", "m1")),
	}
	e.LoadTable = func() ([]mountinfo.Mount, error) { return table, nil }
	for _, tc := range []struct {
		query   Query
		want    []string
		orphans int
	}{
		{Query{}, []string{"closed", "missing", "residual"}, 1},
		{Query{Activity: ActivityActive}, []string{"missing", "residual"}, 1},
		{Query{Activity: ActivityInactive}, []string{"closed"}, 0},
		{Query{Activity: ActivityActive, Session: "closed"}, nil, 0},
		{Query{Activity: ActivityActive, Session: "missing", Directory: point, Image: "alice/app"}, []string{"missing"}, 0},
		{Query{Activity: ActivityInactive, Directory: point, Image: "alice/app"}, []string{"closed"}, 0},
		{Query{Activity: ActivityActive, Directory: point + "-unrelated"}, nil, 0},
	} {
		got, err := e.Query("missing", tc.query)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, view := range got.Sessions {
			names = append(names, view.Name)
			if view.Name == "missing" && (len(view.Actions) != 2 || len(view.Mounts) != 2 || !view.Current) {
				t.Fatalf("incomplete selected view: %+v", view)
			}
		}
		if !reflect.DeepEqual(names, tc.want) || len(got.Orphans) != tc.orphans {
			t.Fatalf("Query(%+v): sessions=%v, orphans=%d; want %v, %d", tc.query, names, len(got.Orphans), tc.want, tc.orphans)
		}
	}
	for _, activity := range []ActivityClass{ActivityActive, ActivityInactive} {
		if _, err := e.Query("", Query{Session: "absent", Activity: activity}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing session with %s: %v", activity, err)
		}
	}
}

func TestQueryActivityRetainsUnknownAndRejectsInvalidSelectors(t *testing.T) {
	e, _ := lookupEngine(t, nil, nil)
	seedMounted(t, e, "busy", "/view")
	e.Lock = func(string, time.Duration) (func(), error) { return nil, errors.New("busy") }
	for _, activity := range []ActivityClass{ActivityActive, ActivityInactive} {
		got, err := e.Query("", Query{Session: "busy", Activity: activity})
		if err != nil || len(got.Sessions) != 1 || got.Sessions[0].ActivityClass != ActivityUnknown {
			t.Fatalf("unknown with %s: %+v, %v", activity, got, err)
		}
	}
	loads := 0
	e.LoadTable = func() ([]mountinfo.Mount, error) { loads++; return nil, nil }
	for _, activity := range []ActivityClass{ActivityUnknown, "invalid"} {
		if _, err := e.Query("", Query{Activity: activity}); err == nil {
			t.Fatalf("accepted selector %q", activity)
		}
	}
	if loads != 0 {
		t.Fatal("invalid selectors loaded the host snapshot")
	}
}
