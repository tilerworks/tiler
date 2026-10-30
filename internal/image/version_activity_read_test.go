package image

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jiangxin/tiler/internal/activity"
)

func TestReadVersionLastActivityLeavesLogsUnchanged(t *testing.T) {
	repo := testRepo(t)
	node := versionNode("read activity")
	w, err := openVersionLog(repo, node.ID, activityClock)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Append(activity.Event{Event: activity.EventMount, Phase: activity.PhaseSucceeded}); err != nil {
		t.Fatal(err)
	}
	path := activityLogPath(t, repo, node.ID)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	at, err := ReadVersionLastActivity(repo, node.ID)
	if err != nil || !at.Equal(testActivityNow) {
		t.Fatalf("last activity = %v, %v; want %v", at, err, testActivityNow)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("log changed: %q, %v", after, err)
	}
}

func TestReadVersionLastActivityHandlesMissingAndDamagedLogs(t *testing.T) {
	for _, tc := range []struct {
		name string
		body []byte
		want error
	}{
		{name: "missing directory", want: activity.ErrNoActivity},
		{name: "missing file", want: activity.ErrNoActivity},
		{name: "empty log", body: []byte{}, want: activity.ErrNoActivity},
		{name: "damaged log", body: []byte("broken\n"), want: activity.ErrDamagedLog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := testRepo(t)
			node := versionNode(tc.name)
			path := activityLogPath(t, repo, node.ID)
			if tc.name == "missing file" {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if tc.body != nil {
				if err := writeActivityBytes(t, repo, node.ID, tc.body); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := ReadVersionLastActivity(repo, node.ID); !errors.Is(err, tc.want) {
				t.Fatalf("read error = %v, want %v", err, tc.want)
			}
			if tc.body == nil {
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("missing log was created: %v", err)
				}
			} else if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, tc.body) {
				t.Fatalf("log changed: %q, %v", got, err)
			}
			if tc.name == "missing directory" {
				if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("missing directory was created: %v", err)
				}
			}
		})
	}
}

func TestReadVersionLastActivityRejectsUnsafeFiles(t *testing.T) {
	repo := testRepo(t)
	node := versionNode("unsafe log")
	path := activityLogPath(t, repo, node.ID)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadVersionLastActivity(repo, node.ID); !errors.Is(err, activity.ErrUnsafeFile) {
		t.Fatalf("read error = %v, want unsafe file", err)
	}
	if _, err := ReadVersionLastActivity(repo, "../invalid"); err == nil || errors.Is(err, activity.ErrNoActivity) {
		t.Fatalf("invalid version error = %v", err)
	}
}
