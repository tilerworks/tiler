package cli

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/image"
	"github.com/jiangxin/tiler/internal/session"
)

// Pin golden output independently of the machine running the tests.
func TestMain(m *testing.M) {
	time.Local = time.UTC
	os.Exit(m.Run())
}

func TestTextTimesUseLocalSecondsWithoutChangingJSON(t *testing.T) {
	previous := time.Local
	time.Local = time.FixedZone("test", 8*60*60)
	t.Cleanup(func() { time.Local = previous })
	at := time.Date(2026, 10, 30, 23, 0, 29, 719842682, time.UTC)
	want := "2026-10-31 07:00:29"
	if got := formatTextTime(at); got != want {
		t.Fatalf("local timestamp = %q, want %q", got, want)
	}
	if got := formatTextTime(time.Time{}); got != "-" {
		t.Fatalf("missing timestamp = %q", got)
	}
	node := versionNode("a", image.KindFull, "", at)
	view := versionImageView{Name: "app", Latest: &node, Images: []image.ImageMeta{node}, Selected: &node, Stack: []image.ImageMeta{node}}
	renders := map[string]func(io.Writer) error{
		"images list": func(w io.Writer) error { return writeVersionImageTable(w, []versionImageView{view}) },
		"images show": func(w io.Writer) error { return writeVersionImageDetail(w, view) },
		"session list": func(w io.Writer) error {
			return writeSessionList(w, []session.Summary{{Name: "dev", Created: at}})
		},
		"session show": func(w io.Writer) error {
			return writeSessionShow(w, session.Record{Name: "dev", CreatedAt: at, Actions: []session.Action{{Timestamp: at}}})
		},
		"status": func(w io.Writer) error {
			return writeSessionBlock(w, session.View{Summary: session.Summary{Name: "dev", Created: at}})
		},
	}
	for name, render := range renders {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			if err := render(&out); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), want) || strings.Contains(out.String(), ".719842682") {
				t.Fatalf("unexpected text timestamp: %s", &out)
			}
		})
	}
	if got := formatGCStamp(at); got != want {
		t.Fatalf("GC timestamp = %q, want %q", got, want)
	}
	if got := newVersionImageJSON(view).Images[0].CreatedAt; got != "2026-10-30T23:00:29.719842682Z" {
		t.Fatalf("JSON timestamp changed: %q", got)
	}
}
