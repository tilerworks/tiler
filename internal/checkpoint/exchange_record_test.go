package checkpoint

import (
	"encoding/json"
	"testing"
	"time"
)

func TestExchangeRecordValidation(t *testing.T) {
	base := exchangeRecord{Version: 3, Protocol: "exchange", Operation: "create", Target: "/home/user/.agents", Temporary: "/home/user/..agents.tiler-checkpoint-123", HadTarget: true, Original: restoreIdentity{Device: 1, Inode: 2}, CreatedAt: time.Unix(1, 0), Phase: "intent"}
	valid := []exchangeRecord{base}
	copyRecord := base
	copyRecord.Copy = restoreIdentity{Device: 1, Inode: 3}
	for _, phase := range []string{"copy", "publish", "exchange", "marker", "complete"} {
		copyRecord.Phase = phase
		valid = append(valid, copyRecord)
	}
	restore := exchangeRecord{Version: 3, Protocol: "placeholder", Operation: "restore", Target: base.Target, Archive: base.Target + "-20261123-120000", HadTarget: true, Original: base.Original, Copy: copyRecord.Copy, Placeholder: restoreIdentity{Device: 1, Inode: 4}, CreatedAt: base.CreatedAt}
	for _, phase := range []string{"prepared", "reserve", "install", "cleanup", "marker", "archive", "remove", "complete"} {
		restore.Phase = phase
		restore.Remove = phase == "remove"
		valid = append(valid, restore)
	}
	missing := restore
	missing.HadTarget, missing.Copy, missing.Archive, missing.Placeholder = false, restoreIdentity{}, "", restoreIdentity{}
	valid = append(valid, missing)
	for _, rec := range valid {
		data, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = parseExchangeRecord(data, rec.Target); err != nil {
			t.Fatalf("%s/%s: %v", rec.Operation, rec.Phase, err)
		}
		if _, err = parseExchangeRecord(append(data, []byte(" {}")...), rec.Target); err == nil {
			t.Fatal("accepted trailing JSON")
		}
		if _, err = parseExchangeRecord(data, "/other"); err == nil {
			t.Fatal("accepted wrong target")
		}
	}
	invalid := []exchangeRecord{base, base, base, copyRecord, restore, restore}
	invalid[0].Version = 1
	invalid[1].Temporary = "/tmp/foreign"
	invalid[2].Phase = "future"
	invalid[3].Copy = invalid[3].Original
	invalid[4].Archive = base.Target + "-invalid"
	invalid[5].Operation = "unknown"
	for _, rec := range invalid {
		if err := rec.validate(base.Target); err == nil {
			t.Fatalf("accepted invalid record: %+v", rec)
		}
	}
	if _, err := parseExchangeRecord([]byte(`{"unknown":true}`), base.Target); err == nil {
		t.Fatal("accepted unknown field")
	}
}

func TestRestoreExchangePhaseOptions(t *testing.T) {
	base := exchangeRecord{Version: 3, Protocol: "placeholder", Operation: "restore", Target: "/home/user/.agents", Archive: "/home/user/.agents-20261123-120000", HadTarget: true, Original: restoreIdentity{Device: 1, Inode: 2}, Copy: restoreIdentity{Device: 1, Inode: 3}, Placeholder: restoreIdentity{Device: 1, Inode: 4}, CreatedAt: time.Unix(1, 0)}
	for _, tt := range []struct {
		name      string
		phase     string
		hadTarget bool
		remove    bool
		valid     bool
	}{
		{"archive with target", "archive", true, false, true},
		{"archive without target", "archive", false, false, false},
		{"remove with target and option", "remove", true, true, true},
		{"remove without option", "remove", true, false, false},
		{"remove without target", "remove", false, true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := base
			rec.Phase, rec.HadTarget, rec.Remove = tt.phase, tt.hadTarget, tt.remove
			if !tt.hadTarget {
				rec.Copy, rec.Archive, rec.Placeholder = restoreIdentity{}, "", restoreIdentity{}
			}
			data, err := json.Marshal(rec)
			if err != nil {
				t.Fatal(err)
			}
			_, err = parseExchangeRecord(data, rec.Target)
			if (err == nil) != tt.valid {
				t.Fatalf("valid=%t, error=%v", tt.valid, err)
			}
		})
	}
}

func TestExchangeRecordStageValidation(t *testing.T) {
	base := exchangeRecord{Version: 3, Protocol: "exchange", Operation: "create", Target: "/home/user/.agents", Temporary: "/home/user/..agents.tiler-checkpoint-copy", HadTarget: true, Original: restoreIdentity{Device: 1, Inode: 2}, CreatedAt: time.Unix(1, 0), Phase: "intent"}
	stage := exchangeStage{Path: "/home/user/..agents.tiler-checkpoint-stage-random", Purpose: "probe"}
	base.Stage = &stage
	check := func(t *testing.T, rec exchangeRecord, valid bool) {
		t.Helper()
		data, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		_, err = parseExchangeRecord(data, rec.Target)
		if (err == nil) != valid {
			t.Fatalf("valid=%t: %v", valid, err)
		}
	}
	check(t, base, true)
	stage.Identity = restoreIdentity{Device: 1, Inode: 4}
	check(t, base, true)
	base.Copy, base.Phase, stage.Purpose = restoreIdentity{Device: 1, Inode: 3}, "marker", "marker"
	check(t, base, true)
	for _, tc := range []struct {
		name string
		edit func(*exchangeRecord, *exchangeStage)
	}{
		{"foreign directory", func(_ *exchangeRecord, s *exchangeStage) { s.Path = "/tmp/..agents.tiler-checkpoint-stage-x" }},
		{"empty suffix", func(_ *exchangeRecord, s *exchangeStage) { s.Path = "/home/user/..agents.tiler-checkpoint-stage-" }},
		{"unrelated name", func(_ *exchangeRecord, s *exchangeStage) { s.Path = "/home/user/unrelated" }},
		{"wrong phase", func(r *exchangeRecord, _ *exchangeStage) { r.Phase = "exchange" }},
		{"wrong purpose", func(_ *exchangeRecord, s *exchangeStage) { s.Purpose = "probe" }},
		{"partial identity", func(_ *exchangeRecord, s *exchangeStage) { s.Identity = restoreIdentity{Device: 1} }},
		{"different device", func(_ *exchangeRecord, s *exchangeStage) { s.Identity.Device = 2 }},
		{"original identity", func(r *exchangeRecord, s *exchangeStage) { s.Identity = r.Original }},
		{"copy identity", func(r *exchangeRecord, s *exchangeStage) { s.Identity = r.Copy }},
		{"restore operation", func(r *exchangeRecord, _ *exchangeStage) { r.Operation = "restore" }},
		{"temporary collision", func(r *exchangeRecord, s *exchangeStage) { r.Temporary = s.Path }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, s := base, stage
			r.Stage = &s
			tc.edit(&r, &s)
			check(t, r, false)
		})
	}
}
