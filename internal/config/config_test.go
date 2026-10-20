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
	if cfg.Version != CurrentConfigVersion {
		t.Fatalf("version = %d, want %d", cfg.Version, CurrentConfigVersion)
	}
	if cfg.ImagesDir != filepath.Join(root, "images") {
		t.Fatalf("images dir = %q", cfg.ImagesDir)
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
	if cfg.SplitSize != 0 || cfg.CacheEnabled {
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
	base.SplitSize = 4096
	base.LogLevel = "warn"

	patch, err := Decode([]byte(strings.Join([]string{
		"keep_versions: 0",
		"max_age: 0s",
		"session_max_age: 0",
		"cache_enabled: false",
		"split_size: 0",
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
	if got.CacheEnabled || got.SplitSize != 0 {
		t.Fatalf("explicit false or zero not applied: %+v", got)
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

func TestDecodeRejectsRemovedRuntimeDir(t *testing.T) {
	_, err := Decode([]byte("runtime_dir: ~/.tiler/runtime\n"))
	if err == nil {
		t.Fatal("removed runtime_dir key was accepted")
	}
	if !strings.Contains(err.Error(), "runtime_dir") {
		t.Fatalf("error = %v, want the field name", err)
	}
}

func TestDecodeAcceptsVersion(t *testing.T) {
	patch, err := Decode([]byte("version: 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	if patch.Version == nil || *patch.Version != 1 {
		t.Fatalf("version = %v, want 1", patch.Version)
	}
	got := patch.Apply(Defaults(testHome))
	if got.Version != 1 {
		t.Fatalf("Apply version = %d", got.Version)
	}
}

func TestDecodeRejectsNonIntegerVersion(t *testing.T) {
	for _, doc := range []string{
		"version: 1.5\n",
		"version: true\n",
		"version:\n  - 1\n",
	} {
		_, err := Decode([]byte(doc))
		if err == nil {
			t.Fatalf("accepted %q", doc)
		}
		if !strings.Contains(err.Error(), "version") {
			t.Fatalf("error = %v, want version", err)
		}
	}
}

func TestVersionOverlay(t *testing.T) {
	base := Defaults(testHome)
	two := 2
	got := Patch{Version: &two}.Apply(base)
	if got.Version != 2 {
		t.Fatalf("Apply = %d, want 2", got.Version)
	}
	got = Patch{}.Apply(base)
	if got.Version != CurrentConfigVersion {
		t.Fatalf("omitted version changed to %d", got.Version)
	}
}

func TestInvalidConfig(t *testing.T) {
	cases := []struct {
		name  string
		doc   string
		field string
	}{
		{name: "negative version", doc: "version: -1\n", field: "version"},
		{name: "unknown log level", doc: "log_level: verbose\n", field: "log_level"},
		{name: "negative keep versions", doc: "keep_versions: -1\n", field: "keep_versions"},
		{name: "fractional keep versions", doc: "keep_versions: 0.9\n", field: "keep_versions"},
		{name: "negative fractional keep versions", doc: "keep_versions: -0.5\n", field: "keep_versions"},
		{name: "negative max age", doc: "max_age: -1s\n", field: "max_age"},
		{name: "non-scalar max age", doc: "max_age:\n  - 1h\n", field: "max_age"},
		{name: "negative day max age", doc: "max_age: -2d\n", field: "max_age"},
		{name: "negative session max age", doc: "session_max_age: -1h\n", field: "session_max_age"},
		{name: "negative split size", doc: "split_size: -1\n", field: "split_size"},
		{name: "fractional split size", doc: "split_size: 1.5\n", field: "split_size"},
		{name: "iec split size", doc: "split_size: 2ki\n", field: "split_size"},
		{name: "unknown split suffix", doc: "split_size: 2p\n", field: "split_size"},
		{name: "non-scalar split size", doc: "split_size:\n  - 2G\n", field: "split_size"},
		{name: "removed split_enabled", doc: "split_enabled: true\n", field: "split_enabled"},
		{name: "removed split_mode", doc: "split_mode: blocks\n", field: "split_mode"},
		{name: "removed split_route", doc: "split_route: concat\n", field: "split_route"},
		{name: "unknown image format", doc: "image_format: tar\n", field: "image_format"},
		{name: "erofs rejects xz", doc: "image_format: erofs\nimage_compression: xz\n", field: "image_compression"},
		{name: "unknown compression", doc: "image_compression: bzip2\n", field: "image_compression"},
		{name: "erofs options are not a key", doc: "erofs_options: foo\n", field: "erofs_options"},
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

func TestSplitSizeYAML(t *testing.T) {
	got, err := applyDoc("split_size: 2G\n")
	if err != nil {
		t.Fatal(err)
	}
	if got.SplitSize != 2<<30 {
		t.Fatalf("split_size = %d, want %d", got.SplitSize, int64(2)<<30)
	}

	got, err = applyDoc("log_level: debug\n")
	if err != nil {
		t.Fatal(err)
	}
	if got.SplitSize != 0 {
		t.Fatalf("omitted split_size = %d, want 0", got.SplitSize)
	}

	// An explicit zero in a nearer layer clears a lower value.
	base := Defaults(testHome)
	base.SplitSize = 2 << 30
	patch, err := Decode([]byte("split_size: 0\n"))
	if err != nil {
		t.Fatal(err)
	}
	if patch.SplitSize == nil || *patch.SplitSize != 0 {
		t.Fatal("explicit zero split_size was omitted")
	}
	got = patch.Apply(base)
	if err := Validate(got); err != nil {
		t.Fatal(err)
	}
	if got.SplitSize != 0 {
		t.Fatalf("explicit zero did not clear the base: %+v", got)
	}
}

func TestImageContainerOverlay(t *testing.T) {
	got, err := applyDoc("image_format: erofs\nimage_compression: lz4\n")
	if err != nil {
		t.Fatal(err)
	}
	if got.ImageFormat != "erofs" || got.ImageCompression != "lz4" {
		t.Fatalf("container = %s/%s", got.ImageFormat, got.ImageCompression)
	}
	base := got
	patch, err := Decode([]byte("image_format: squashfs\n"))
	if err != nil {
		t.Fatal(err)
	}
	if patch.ImageCompression != nil {
		t.Fatal("omitted image_compression was marked present")
	}
	got = patch.Apply(base)
	if err := Validate(got); err != nil {
		t.Fatal(err)
	}
	if got.ImageFormat != "squashfs" || got.ImageCompression != "lz4" {
		t.Fatalf("overlay = %s/%s, want squashfs/lz4", got.ImageFormat, got.ImageCompression)
	}
	if Defaults(testHome).ImageFormat != "" || Defaults(testHome).ImageCompression != "" {
		t.Fatal("defaults set a container")
	}
}

func TestValidateRejectsNegativeSplitSize(t *testing.T) {
	cfg := Defaults(testHome)
	cfg.SplitSize = -1
	err := Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "split_size") {
		t.Fatalf("error = %v", err)
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
