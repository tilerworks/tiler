package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testHome = "/injected/home"

func TestDefaults(t *testing.T) {
	cfg := Defaults(testHome)
	root := filepath.Join(testHome, ".tiler")
	if cfg.ImagesDir != filepath.Join(root, "images") {
		t.Fatalf("images dir = %q", cfg.ImagesDir)
	}
	if cfg.RuntimeDir != filepath.Join(root, "runtime") {
		t.Fatalf("runtime dir = %q", cfg.RuntimeDir)
	}
	if cfg.SessionsDir != filepath.Join(root, "sessions") {
		t.Fatalf("sessions dir = %q", cfg.SessionsDir)
	}
	if cfg.CacheDir != filepath.Join(root, "cache") {
		t.Fatalf("cache dir = %q", cfg.CacheDir)
	}
	if cfg.LogLevel != DefaultLogLevel {
		t.Fatalf("log level = %q", cfg.LogLevel)
	}
	if cfg.SplitEnabled || cfg.SplitMode != "" || cfg.SplitRoute != "" || cfg.CacheEnabled {
		t.Fatalf("split or cache default is set: %+v", cfg)
	}
	if cfg.SessionMaxAge != DefaultSessionMaxAge {
		t.Fatalf("session max age = %s", cfg.SessionMaxAge)
	}
	if cfg.SessionMaxAge != 720*time.Hour {
		t.Fatalf("session max age = %s, want 720h", cfg.SessionMaxAge)
	}
	if cfg.SessionAutocloseStale {
		t.Fatalf("session autoclose defaults on: %+v", cfg)
	}
	if cfg.KeepVersions != 0 || cfg.MaxAge != 0 {
		t.Fatalf("image limits = %d %s", cfg.KeepVersions, cfg.MaxAge)
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("defaults: %v", err)
	}
}

func TestParseDurationDaysEqualHours(t *testing.T) {
	days, err := ParseDuration("30d")
	if err != nil {
		t.Fatal(err)
	}
	hours, err := ParseDuration("720h")
	if err != nil {
		t.Fatal(err)
	}
	if days != hours {
		t.Fatalf("30d = %s, 720h = %s", days, hours)
	}
	if days != DefaultSessionMaxAge {
		t.Fatalf("30d = %s, want %s", days, DefaultSessionMaxAge)
	}
}

func TestExplicitZeroDiffersFromDefault(t *testing.T) {
	base := Defaults(testHome)
	base.KeepVersions = 4
	base.MaxAge = 10 * time.Hour
	base.SessionMaxAge = 48 * time.Hour
	base.CacheEnabled = true
	base.LogLevel = "warn"

	patch, err := Decode([]byte(strings.Join([]string{
		"keep_versions: 0",
		"max_age: 0s",
		"session_max_age: 0",
		"cache_enabled: false",
		"split_enabled: false",
	}, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	if patch.KeepVersions == nil || *patch.KeepVersions != 0 {
		t.Fatal("explicit zero keep_versions was omitted")
	}
	if patch.CacheEnabled == nil || *patch.CacheEnabled {
		t.Fatal("explicit false cache_enabled was omitted")
	}
	if patch.SessionMaxAge == nil || *patch.SessionMaxAge != 0 {
		t.Fatal("explicit zero session_max_age was omitted")
	}
	if patch.LogLevel != nil {
		t.Fatal("omitted log_level was marked present")
	}

	got := patch.Apply(base)
	if err := Validate(got); err != nil {
		t.Fatal(err)
	}
	if got.KeepVersions != 0 || got.MaxAge != 0 || got.SessionMaxAge != 0 {
		t.Fatalf("explicit zeros not applied: %+v", got)
	}
	if got.CacheEnabled || got.SplitEnabled {
		t.Fatalf("explicit false not applied: %+v", got)
	}
	if got.LogLevel != "warn" {
		t.Fatalf("omitted log level changed to %q", got.LogLevel)
	}
	if Defaults(testHome).SessionMaxAge == got.SessionMaxAge {
		t.Fatal("explicit zero session max age collapsed into the default")
	}
}

func TestSessionAutocloseStaleOverlay(t *testing.T) {
	on := true
	off := false
	tests := []struct {
		name  string
		base  bool
		patch *bool
		want  bool
	}{
		{name: "default off stays off", base: false, patch: nil, want: false},
		{name: "explicit on", base: false, patch: &on, want: true},
		{name: "explicit off overrides on", base: true, patch: &off, want: false},
		{name: "omitted keeps base", base: true, patch: nil, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base := Defaults(testHome)
			base.SessionAutocloseStale = tc.base
			got := Patch{SessionAutocloseStale: tc.patch}.Apply(base)
			if got.SessionAutocloseStale != tc.want {
				t.Fatalf("Apply() = %v, want %v", got.SessionAutocloseStale, tc.want)
			}
		})
	}
	patch, err := Decode([]byte("session_autoclose_stale: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	if patch.SessionAutocloseStale == nil || !*patch.SessionAutocloseStale {
		t.Fatal("explicit true was omitted")
	}
	patch, err = Decode([]byte("session_autoclose_stale: false\n"))
	if err != nil {
		t.Fatal(err)
	}
	if patch.SessionAutocloseStale == nil || *patch.SessionAutocloseStale {
		t.Fatal("explicit false was omitted")
	}
	patch, err = Decode([]byte("log_level: info\n"))
	if err != nil {
		t.Fatal(err)
	}
	if patch.SessionAutocloseStale != nil {
		t.Fatal("omitted key was marked present")
	}
}

func TestOmittedDocumentKeepsBase(t *testing.T) {
	base := Defaults(testHome)
	base.LogLevel = "debug"
	patch, err := Decode(nil)
	if err != nil {
		t.Fatal(err)
	}
	got := patch.Apply(base)
	if got.LogLevel != "debug" || got.SessionMaxAge != DefaultSessionMaxAge {
		t.Fatalf("empty document changed config: %+v", got)
	}
}

func TestDecodeUnknownKey(t *testing.T) {
	_, err := Decode([]byte("image_dir: /tmp\n"))
	if err == nil {
		t.Fatal("unknown key was accepted")
	}
	if !strings.Contains(err.Error(), "image_dir") {
		t.Fatalf("error = %v, want the field name", err)
	}
}

func TestInvalidConfig(t *testing.T) {
	cases := []struct {
		name  string
		doc   string
		field string
	}{
		{name: "unknown log level", doc: "log_level: verbose\n", field: "log_level"},
		{name: "negative keep versions", doc: "keep_versions: -1\n", field: "keep_versions"},
		{name: "fractional keep versions", doc: "keep_versions: 0.9\n", field: "keep_versions"},
		{name: "negative fractional keep versions", doc: "keep_versions: -0.5\n", field: "keep_versions"},
		{name: "negative max age", doc: "max_age: -1s\n", field: "max_age"},
		{name: "non-scalar max age", doc: "max_age:\n  - 1h\n", field: "max_age"},
		{name: "negative day max age", doc: "max_age: -2d\n", field: "max_age"},
		{name: "negative session max age", doc: "session_max_age: -1h\n", field: "session_max_age"},
		{name: "split enabled without mode", doc: "split_enabled: true\n", field: "split_enabled"},
		{
			name:  "split enabled with unknown mode",
			doc:   "split_enabled: true\nsplit_mode: shards\n",
			field: "split_mode",
		},
		{name: "mode set while split is off", doc: "split_mode: blocks\n", field: "split_mode"},
		{
			name:  "mode set while split is explicitly off",
			doc:   "split_enabled: false\nsplit_mode: members\n",
			field: "split_mode",
		},
		{
			name:  "route set while split is off",
			doc:   "split_route: concat\n",
			field: "split_route",
		},
		{
			name:  "route with members mode",
			doc:   "split_enabled: true\nsplit_mode: members\nsplit_route: concat\n",
			field: "split_route",
		},
		{
			name:  "unknown route",
			doc:   "split_enabled: true\nsplit_mode: blocks\nsplit_route: splice\n",
			field: "split_route",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := applyDoc(tc.doc)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("error = %v, want field %s", err, tc.field)
			}
		})
	}
}

func TestValidSplitModes(t *testing.T) {
	for _, mode := range []string{SplitModeBlocks, SplitModeMembers} {
		got, err := applyDoc("split_enabled: true\nsplit_mode: " + mode + "\n")
		if err != nil {
			t.Fatal(err)
		}
		if !got.SplitEnabled || got.SplitMode != mode {
			t.Fatalf("mode %s: %+v", mode, got)
		}
	}
}

func TestValidSplitRoutes(t *testing.T) {
	// The omitted and the explicit empty route both keep the default
	// reassemble behavior; the two named routes pin the mount form.
	for _, route := range []string{"", SplitRouteReassemble, SplitRouteConcat} {
		doc := "split_enabled: true\nsplit_mode: blocks\n"
		if route != "" {
			doc += "split_route: " + route + "\n"
		}
		got, err := applyDoc(doc)
		if err != nil {
			t.Fatal(err)
		}
		if got.SplitRoute != route {
			t.Fatalf("route %q: %+v", route, got)
		}
	}

	// An explicit empty route in a nearer layer clears a lower value.
	base := Defaults(testHome)
	base.SplitEnabled = true
	base.SplitMode = SplitModeBlocks
	base.SplitRoute = SplitRouteConcat
	patch, err := Decode([]byte("split_route: \"\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if patch.SplitRoute == nil || *patch.SplitRoute != "" {
		t.Fatal("explicit empty split_route was omitted")
	}
	got := patch.Apply(base)
	if err := Validate(got); err != nil {
		t.Fatal(err)
	}
	if got.SplitRoute != "" {
		t.Fatalf("explicit empty route did not clear the base: %+v", got)
	}
}

func applyDoc(doc string) (Config, error) {
	patch, err := Decode([]byte(doc))
	if err != nil {
		return Config{}, err
	}
	cfg := patch.Apply(Defaults(testHome))
	if err := Validate(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}
