package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/activity"
	"github.com/jiangxin/tiler/internal/config"
	"github.com/jiangxin/tiler/internal/image"
)

func TestImageListShowsPropagatedActivityWithoutRenewingLogs(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{ImagesDir: filepath.Join(root, "images"), LogsDir: filepath.Join(root, "custom-logs")}
	base, mid, tip, sibling, _ := mountActivityLibrary(t, cfg.ImagesDir)
	repo, err := image.OpenRepo(cfg.ImagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	repo.LogsDir = cfg.LogsDir
	at := time.Now().Add(-time.Hour)
	if err := image.WithListedImageLocks(cfg.ImagesDir, time.Second, func(image.Hold) error {
		return image.AppendVersionActivityLocked(repo, tip.ID, activity.Event{
			Event: activity.EventMount, Phase: activity.PhaseSucceeded,
		}, func() time.Time { return at })
	}); err != nil {
		t.Fatal(err)
	}
	before := make(map[string][]byte)
	for _, node := range []image.ImageMeta{base, mid, tip} {
		path := filepath.Join(cfg.LogsDir, "images", "alice", "app", node.ID+".log")
		before[path], err = os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	var out, warnings bytes.Buffer
	if err := listAllImages(&out, &warnings, cfg, false, defaultOutputWidth, true); err != nil {
		t.Fatal(err)
	}
	if warnings.Len() != 0 || strings.Count(out.String(), formatImageListTime(at, time.Now())) != 3 {
		t.Fatalf("output = %q, warnings = %q", out.String(), warnings.String())
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.Contains(line, sibling.ID[:10]) && strings.Contains(line, formatImageListTime(at, time.Now())) {
			t.Fatalf("sibling received activity: %q", line)
		}
	}
	out.Reset()
	if err := listAllImages(&out, &warnings, cfg, false, defaultOutputWidth, false); err != nil || strings.Count(out.String(), formatImageListTime(at, time.Now())) != 1 {
		t.Fatalf("aggregate activity: %v %s", err, &out)
	}
	for path, data := range before {
		if got, err := os.ReadFile(path); err != nil || !bytes.Equal(data, got) {
			t.Fatalf("log renewed: %q, %v", got, err)
		}
	}
}

func TestImageListIsolatesActivityFailuresAndMissingLogs(t *testing.T) {
	for _, state := range []string{"missing directory", "missing file", "empty", "damaged", "unsafe"} {
		t.Run(state, func(t *testing.T) {
			root := t.TempDir()
			cfg := config.Config{ImagesDir: filepath.Join(root, "images"), LogsDir: filepath.Join(root, "logs")}
			node := versionNode("a", image.KindFull, "", time.Now().UTC())
			other := versionNode("b", image.KindFull, "", node.CreatedAt)
			writeVersionForest(t, cfg.ImagesDir, "alice/app", node, other)
			path := filepath.Join(cfg.LogsDir, "images", "alice", "app", node.ID+".log")
			if state != "missing directory" {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if state == "empty" || state == "damaged" {
				body := ""
				if state == "damaged" {
					body = "broken\n"
				}
				if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if state == "unsafe" {
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			var out, warnings bytes.Buffer
			cmd := newImagesListCmd()
			cmd.SetContext(context.Background())
			remember(cmd, cfg)
			cmd.SetOut(&out)
			cmd.SetErr(&warnings)
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			rows := strings.Split(strings.TrimSpace(out.String()), "\n")
			if len(rows) != 2 || strings.Fields(rows[1])[1] != "2" {
				t.Fatalf("expected both versions in summary: %s", &out)
			}
			bad := state == "damaged" || state == "unsafe"
			if bad != strings.Contains(out.String(), "unknown") || bad != strings.Contains(warnings.String(), "alice/app:"+node.ID) {
				t.Fatalf("output = %q, warnings = %q", out.String(), warnings.String())
			}
			if state == "missing directory" {
				if _, err := os.Stat(cfg.LogsDir); !os.IsNotExist(err) {
					t.Fatalf("logs directory created: %v", err)
				}
			}
			failure := errors.New("broken output")
			if err := listAllImages(failWriter{failure}, io.Discard, cfg, false, defaultOutputWidth, true); !errors.Is(err, failure) {
				t.Fatalf("output error = %v", err)
			}
			if bad {
				if err := listAllImages(io.Discard, failWriter{failure}, cfg, false, defaultOutputWidth, true); !errors.Is(err, failure) {
					t.Fatalf("warning error = %v", err)
				}
			}
			out.Reset()
			warnings.Reset()
			if err := listAllImages(&out, &warnings, cfg, true, defaultOutputWidth, true); err != nil || warnings.Len() != 0 || strings.Contains(out.String(), "last_activity") {
				t.Fatalf("JSON changed: %q, %q, %v", out.String(), warnings.String(), err)
			}
			if err := showOneImage(io.Discard, cfg, "alice/app", false); err != nil {
				t.Fatal(err)
			}
		})
	}
}
