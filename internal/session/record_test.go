package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/image"
)

// sampleAction is one fully populated mount action for the round
// trip and contract tests.
func sampleAction(seq int) Action {
	return Action{
		Seq:        seq,
		Type:       TypeMount,
		Image:      "alice/app",
		Name:       "alice/app",
		FullID:     "0123456789abcdef",
		Mountpoint: "/tmp/view",
		Mode:       ModeRW,
		Subdir:     "src",
		RuntimeID:  runtimeIDOf(seq),
		Status:     StatusMounted,
		Timestamp:  time.Date(2026, 10, 17, 3, 15, 0, 0, time.UTC),
	}
}

// actionEqual compares two actions, treating timestamps by instant.
func actionEqual(got, want Action) bool {
	if !got.Timestamp.Equal(want.Timestamp) {
		return false
	}
	got.Timestamp = time.Time{}
	want.Timestamp = time.Time{}
	return got == want
}

func TestRecordJSONRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := Store{Dir: dir}
	rec := Record{
		Name:      "dev",
		State:     StateClosed,
		Actions:   []Action{sampleAction(1), sampleAction(2)},
		CreatedAt: time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC),
		ClosedAt:  time.Date(2026, 10, 17, 3, 0, 0, 0, time.UTC),
	}
	if err := store.Save(rec); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load("dev")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Name != rec.Name || loaded.State != rec.State {
		t.Fatalf("header = %s %s", loaded.Name, loaded.State)
	}
	if !loaded.CreatedAt.Equal(rec.CreatedAt) || !loaded.ClosedAt.Equal(rec.ClosedAt) {
		t.Errorf("record times = %v %v", loaded.CreatedAt, loaded.ClosedAt)
	}
	if len(loaded.Actions) != len(rec.Actions) {
		t.Fatalf("actions = %+v", loaded.Actions)
	}
	for i := range rec.Actions {
		if !actionEqual(loaded.Actions[i], rec.Actions[i]) {
			t.Errorf("action %d = %+v, want %+v",
				i, loaded.Actions[i], rec.Actions[i])
		}
	}
}

func TestRecordJSONContractKeys(t *testing.T) {
	dir := t.TempDir()
	store := Store{Dir: dir}
	rec := Record{
		Name:      "dev",
		State:     StateActive,
		Actions:   []Action{sampleAction(1)},
		CreatedAt: time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC),
	}
	if err := store.Save(rec); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "dev", "session.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"name", "state", "actions", "created_at", "closed_at",
	} {
		if _, ok := doc[key]; !ok {
			t.Errorf("record key %q missing", key)
		}
	}
	actions, ok := doc["actions"].([]any)
	if !ok || len(actions) != 1 {
		t.Fatalf("actions = %v", doc["actions"])
	}
	action, ok := actions[0].(map[string]any)
	if !ok {
		t.Fatalf("action = %v", actions[0])
	}
	for _, key := range []string{
		"seq", "type", "image", "name", "full_id",
		"mountpoint", "mode", "subdir", "runtime_id", "status",
		"timestamp",
	} {
		if _, ok := action[key]; !ok {
			t.Errorf("action key %q missing", key)
		}
	}
}

func TestRecordRemoveOnCloseJSON(t *testing.T) {
	dir := t.TempDir()
	store := Store{Dir: dir}
	// A --rm record round-trips its marker and carries the key.
	if err := store.Save(Record{
		Name:          "rm",
		State:         StateActive,
		CreatedAt:     time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC),
		RemoveOnClose: true,
	}); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load("rm")
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.RemoveOnClose {
		t.Fatalf("--rm marker did not round-trip: %+v", loaded)
	}
	data, err := os.ReadFile(filepath.Join(dir, "rm", "session.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["remove_on_close"] != true {
		t.Errorf("remove_on_close = %v, want true", doc["remove_on_close"])
	}
	// A plain record omits the additive key entirely, so existing
	// records and goldens stay byte-for-byte unchanged.
	if err := store.Save(Record{
		Name:      "plain",
		State:     StateActive,
		CreatedAt: time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(filepath.Join(dir, "plain", "session.json"))
	if err != nil {
		t.Fatal(err)
	}
	doc = nil
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc["remove_on_close"]; ok {
		t.Errorf("a plain record carried remove_on_close: %v", doc["remove_on_close"])
	}
}

func TestRecordStackIDsAreAdditive(t *testing.T) {
	dir := t.TempDir()
	store := Store{Dir: dir}
	// An action carrying the desired tip and the served stack ids
	// round-trips all three fields. The ids reuse the package's
	// served-stack fixture constants.
	rebased := sampleAction(1)
	rebased.TipID = fixtureTip
	rebased.ServedID = fixtureServedID
	rebased.ServedTip = fixtureServedTip
	if err := store.Save(Record{
		Name:      "dev",
		State:     StateActive,
		Actions:   []Action{rebased},
		CreatedAt: time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load("dev")
	if err != nil {
		t.Fatal(err)
	}
	if !actionEqual(loaded.Actions[0], rebased) {
		t.Fatalf("action = %+v, want %+v", loaded.Actions[0], rebased)
	}
	action, err := storedActionJSON(t, filepath.Join(dir, "dev", "session.json"))
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{
		"tip_id":     fixtureTip,
		"served_id":  fixtureServedID,
		"served_tip": fixtureServedTip,
	} {
		if action[key] != want {
			t.Errorf("action key %q = %v, want %v", key, action[key], want)
		}
	}
	// An action without stack ids omits the additive keys entirely,
	// so existing records and goldens stay byte-for-byte unchanged.
	if err := store.Save(Record{
		Name:      "plain",
		State:     StateActive,
		Actions:   []Action{sampleAction(1)},
		CreatedAt: time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	action, err = storedActionJSON(t, filepath.Join(dir, "plain", "session.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"tip_id", "served_id", "served_tip"} {
		if _, ok := action[key]; ok {
			t.Errorf("a plain action carried %q: %v", key, action[key])
		}
	}
}

// storedActionJSON reads one record file and returns the JSON object
// of its single action.
func storedActionJSON(t *testing.T, path string) (map[string]any, error) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	actions, ok := doc["actions"].([]any)
	if !ok || len(actions) != 1 {
		t.Fatalf("actions = %v", doc["actions"])
	}
	action, ok := actions[0].(map[string]any)
	if !ok {
		t.Fatalf("action = %v", actions[0])
	}
	return action, nil
}

func TestOldRecordJSONRoundTripsByteIdentical(t *testing.T) {
	dir := t.TempDir()
	store := Store{Dir: dir}
	// A record exactly as it was written before the stack id fields
	// existed: no tip_id, served_id, or served_tip keys anywhere.
	const old = `{
  "name": "dev",
  "state": "active",
  "actions": [
    {
      "seq": 1,
      "type": "mount",
      "image": "alice/app",
      "name": "alice/app",
      "full_id": "0123456789abcdef",
      "mountpoint": "/tmp/view",
      "mode": "rw",
      "subdir": "src",
      "runtime_id": "m1",
      "status": "mounted",
      "timestamp": "2026-10-17T03:15:00Z"
    }
  ],
  "created_at": "2026-10-16T09:00:00Z",
  "closed_at": "0001-01-01T00:00:00Z"
}
`
	if err := os.MkdirAll(filepath.Join(dir, "dev"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "dev", "session.json")
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	rec, err := store.Load("dev")
	if err != nil {
		t.Fatal(err)
	}
	if !actionEqual(rec.Actions[0], sampleAction(1)) {
		t.Fatalf("old action = %+v, want %+v", rec.Actions[0], sampleAction(1))
	}
	if err := store.Save(rec); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != old {
		t.Fatalf("an old record did not round-trip byte-identical:\nwant:\n%s\ngot:\n%s",
			old, after)
	}
}

func TestOldOwnerRepoActionDoesNotPin(t *testing.T) {
	const fullID = "0123456789abcdef"
	raw := `{
		"seq": 1,
		"type": "mount",
		"image": "alice/app",
		"owner": "alice",
		"repo": "app",
		"full_id": "` + fullID + `",
		"mountpoint": "/ws/dev",
		"mode": "rw",
		"status": "mounted",
		"timestamp": "2026-10-17T03:15:00Z"
	}`
	var action Action
	if err := json.Unmarshal([]byte(raw), &action); err != nil {
		t.Fatal(err)
	}
	if action.Name != "" {
		t.Fatalf("Name = %q, want empty after owner/repo-only JSON", action.Name)
	}
	rec := Record{
		Name:    "dev",
		State:   StateActive,
		Actions: []Action{action},
	}
	ref := image.Ref{Name: "alice/app", FullID: fullID}
	if recordPins(rec, ref) {
		t.Fatal("owner/repo-only action pinned the library image")
	}
}

func TestValidateNameMatrix(t *testing.T) {
	valid := []string{
		"dev",
		"my-session_1",
		"auto-20261017T031500Z-deadbeef",
		"x",
		"my.name",
		".hidden",
		"console",
		"COM10",
	}
	for _, name := range valid {
		if err := ValidateName(name); err != nil {
			t.Errorf("ValidateName(%q) = %v", name, err)
		}
	}
	invalid := []string{
		"",
		".",
		"..",
		"a/b",
		`a\b`,
		"a b",
		"a\tb",
		"a\nb",
		"bad\x00name",
		"bad\x1bname",
		" trail",
		"trail ",
		"a:b",
		"a*b",
		"a?b",
		`a"b`,
		"a<b",
		"a>b",
		"a|b",
		"name.",
		"con",
		"CON",
		"nul.txt",
		"aux",
		"prn",
		"com1",
		"COM9",
		"lpt0",
		strings.Repeat("a", sessionNameMax+1),
	}
	for _, name := range invalid {
		err := ValidateName(name)
		if err == nil {
			t.Errorf("ValidateName(%q) accepted", name)
			continue
		}
		if !strings.Contains(err.Error(), "invalid session name") {
			t.Errorf("ValidateName(%q) = %v", name, err)
		}
	}
}

func TestGenerateNameShape(t *testing.T) {
	pattern := regexp.MustCompile(`^[a-z]+-[a-z]+$`)
	digitPattern := regexp.MustCompile(`^[a-z]+-[a-z]+[0-9]$`)
	seen := make(map[string]bool)
	for i := 0; i < 32; i++ {
		name, err := GenerateName()
		if err != nil {
			t.Fatal(err)
		}
		if !pattern.MatchString(name) {
			t.Fatalf("generated name %q does not match %s", name, pattern)
		}
		if err := ValidateName(name); err != nil {
			t.Fatalf("generated name rejected: %v", err)
		}
		seen[name] = true
		withDigit, err := WithCollisionDigit(name)
		if err != nil {
			t.Fatal(err)
		}
		if !digitPattern.MatchString(withDigit) {
			t.Fatalf("name with digit %q does not match %s", withDigit, digitPattern)
		}
		if err := ValidateName(withDigit); err != nil {
			t.Fatalf("name with digit rejected: %v", err)
		}
	}
	if len(seen) < 2 {
		t.Fatalf("GenerateName produced too little variety: %v", seen)
	}
}

func TestRecordValidateRejectsBadShapes(t *testing.T) {
	base := Record{
		Name:      "dev",
		State:     StateActive,
		CreatedAt: time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC),
	}
	tests := []struct {
		name   string
		mutate func(rec *Record)
	}{
		{name: "bad name", mutate: func(rec *Record) { rec.Name = "a/b" }},
		{name: "unknown state", mutate: func(rec *Record) { rec.State = "paused" }},
		{
			name: "unknown action type",
			mutate: func(rec *Record) {
				rec.Actions = []Action{sampleAction(1)}
				rec.Actions[0].Type = "bind"
			},
		},
		{
			name: "unknown action status",
			mutate: func(rec *Record) {
				rec.Actions = []Action{sampleAction(1)}
				rec.Actions[0].Status = "done"
			},
		},
		{
			name: "unknown action mode",
			mutate: func(rec *Record) {
				rec.Actions = []Action{sampleAction(1)}
				rec.Actions[0].Mode = "wx"
			},
		},
		{
			name: "mount without mode",
			mutate: func(rec *Record) {
				rec.Actions = []Action{sampleAction(1)}
				rec.Actions[0].Mode = ""
			},
		},
		{
			name: "zero seq",
			mutate: func(rec *Record) {
				rec.Actions = []Action{sampleAction(0)}
			},
		},
		{
			name: "duplicate seq",
			mutate: func(rec *Record) {
				rec.Actions = []Action{sampleAction(1), sampleAction(1)}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := base
			tc.mutate(&rec)
			if err := rec.Validate(); err == nil {
				t.Fatalf("Validate accepted %+v", rec)
			}
		})
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("empty active record rejected: %v", err)
	}
	// A umount action refers to an earlier mount and needs no mode.
	umountOnly := base
	umountAction := sampleAction(1)
	umountAction.Type = TypeUmount
	umountAction.Mode = ""
	umountAction.Status = StatusUnmounted
	umountOnly.Actions = []Action{umountAction}
	if err := umountOnly.Validate(); err != nil {
		t.Fatalf("umount action without mode rejected: %v", err)
	}
}
