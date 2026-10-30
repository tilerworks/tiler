package session

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/image"
)

func TestQueryCombinesSessionDirectoryAndImageStack(t *testing.T) {
	e, _ := lookupEngine(t, nil, map[string]bool{"/app": true})
	e.ImagesDir = t.TempDir()
	m := sessionVersionManifest()
	saveSessionVersionManifest(t, e.ImagesDir, m)
	rec := versionRecord(t)
	rec.Name = "dev"
	a := &rec.Actions[0]
	a.Mountpoint, a.FullID, a.TipID = "/app", m.Images[0].ID, m.Images[2].ID
	a.ServedID, a.ServedTip = m.Images[0].ID, m.Images[1].ID
	second := *a
	second.Seq, second.Mountpoint, second.Name = 2, "/other", "alice/other"
	rec.Actions = append(rec.Actions, second)
	if err := e.Save(rec); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		query Query
		count int
	}{
		{Query{Session: "dev"}, 1}, {Query{Image: "alice/app"}, 1},
		{Query{Image: "alice/app:aaaa"}, 1}, {Query{Image: "alice/app:bbbb"}, 1},
		{Query{Image: "alice/app:cccc"}, 0}, {Query{Image: "alice/app:dddd"}, 0},
		{Query{Directory: "/other", Image: "alice/app"}, 0},
		{Query{Directory: "/app", Session: "dev", Image: "alice/app:bbbb"}, 1},
	} {
		got, err := e.Query("", tc.query)
		if err != nil || len(got.Sessions) != tc.count {
			t.Fatalf("Query(%+v) = %+v, %v", tc.query, got, err)
		}
		if tc.count > 0 && len(got.Sessions[0].Actions) != 2 {
			t.Fatal("filtered out other session mounts")
		}
	}
	for _, q := range []Query{{Session: "ghost"}, {Session: "bad name"}, {Image: "alice/app:xxx"}} {
		if _, err := e.Query("", q); err == nil {
			t.Fatalf("accepted %+v", q)
		}
	}
	e.Health = func(string) bool { return false }
	if got, err := e.Query("", Query{Image: "alice/app:bbbb"}); err != nil || len(got.Sessions) != 1 {
		t.Fatalf("desired middle layer: %+v, %v", got, err)
	}
	e.Health = func(point string) bool { return point == "/app" }
	repo, _ := image.OpenRepo(e.ImagesDir, m.Name)
	if err := os.Remove(repo.MetaPath()); err != nil {
		t.Fatal(err)
	}
	got, err := e.Query("", Query{Image: "alice/app:" + m.Images[1].ID})
	if err != nil || len(got.Sessions) != 1 || len(got.Diagnostics) == 0 {
		t.Fatalf("missing metadata: %+v, %v", got, err)
	}
	e.Lock = func(string, time.Duration) (func(), error) { return nil, errors.New("busy") }
	got, err = e.Query("", Query{Session: "dev"})
	if err != nil || len(got.Sessions) != 1 || !got.Sessions[0].Locked {
		t.Fatalf("locked session: %+v, %v", got, err)
	}
	got, err = e.Query("", Query{Image: "alice/app"})
	if err != nil || len(got.Sessions) != 0 {
		t.Fatalf("locked image: %+v, %v", got, err)
	}
}
func TestQueryRejectsAmbiguousImagePrefix(t *testing.T) {
	e, _ := lookupEngine(t, nil, nil)
	e.ImagesDir = t.TempDir()
	m := sessionVersionManifest()
	node := m.Images[0]
	node.ID = "aaaa" + m.Images[1].ID[4:]
	node.SHA256 = node.ID
	m.Images = append(m.Images, node)
	saveSessionVersionManifest(t, e.ImagesDir, m)
	_, err := e.Query("", Query{Image: "alice/app:aaaa"})
	var ambiguous *image.AmbiguousVersionError
	if !errors.As(err, &ambiguous) {
		t.Fatalf("ambiguous query: %v", err)
	}
}
