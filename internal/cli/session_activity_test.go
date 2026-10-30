package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/activity"
	"github.com/jiangxin/tiler/internal/mountinfo"
	"github.com/jiangxin/tiler/internal/session"
)

func TestSessionActivityIsSharedByListShowAndStatus(t *testing.T) {
	home, _ := withWorkspace(t)
	_, lib := libFlags(home)
	withMountSeams(t, nil)
	writeGoldenSessionRecords(t, home)
	logs := filepath.Join(home, ".tiler", "logs")
	dir := filepath.Join(logs, "sessions")
	if err := activity.PrepareDir(logs, dir); err != nil {
		t.Fatal(err)
	}
	at := goldenSessionTime.Add(time.Hour)
	writer, err := activity.OpenLog(logs, dir, "dev.log", func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Append(activity.Event{Event: activity.EventSessionOpen, Phase: activity.PhaseFailed, Session: "dev"}); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"session", "list"}, {"session", "ls"}, {"session"}, {"session", "show", "dev"}, {"status"}} {
		out, stderr, _, err := runCmd(t, append(lib, append(args, "--json")...)...)
		if err != nil || stderr != "" {
			t.Fatalf("%v: %v, %s", args, err, stderr)
		}
		if !strings.Contains(out, `"last_activity_at": "2026-10-17T04:15:00Z"`) || !strings.Contains(out, `"last_activity_unknown": false`) {
			t.Fatalf("%v: %s", args, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "dev.log"), []byte("damaged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, stderr, _, err := runCmd(t, append(lib, "session", "show", "dev", "--json")...)
	if err != nil || stderr == "" {
		t.Fatalf("damaged log: %v, %s", err, stderr)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	if doc["last_activity_at"] != nil || doc["last_activity_unknown"] != true {
		t.Fatalf("damaged log: %s", out)
	}
}

func TestSessionListDoesNotInspectHostMounts(t *testing.T) {
	home, _ := withWorkspace(t)
	_, lib := libFlags(home)
	writeGoldenSessionRecords(t, home)
	withSessionSeams(t, func(engine *session.Engine) {
		engine.LoadTable = func() ([]mountinfo.Mount, error) {
			t.Fatal("list inspected host mounts")
			return nil, nil
		}
		engine.LoadDiskImages = func() ([]session.DiskImage, error) {
			t.Fatal("list inspected disk images")
			return nil, nil
		}
	})
	if _, stderr, _, err := runCmd(t, append(lib, "session", "list")...); err != nil {
		t.Fatalf("list: %v, %s", err, stderr)
	}
}
