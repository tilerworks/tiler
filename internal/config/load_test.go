package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadMissingLogsDirUsesDefault(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, "version: 1\nlog_level: info\n")
	got, err := Load(home, home, home, "", Patch{})
	if err != nil {
		t.Fatal(err)
	}
	if got.LogsDir != filepath.Join(home, ".tiler", "logs") {
		t.Fatalf("logs = %q, want the built-in default", got.LogsDir)
	}
}

func TestLoadDefaultsWithoutFiles(t *testing.T) {
	home := t.TempDir()
	start := t.TempDir()
	got, err := Load(start, home, start, "", Patch{})
	if err != nil {
		t.Fatal(err)
	}
	want := Defaults(home)
	if got != want {
		t.Fatalf("Load() = %+v, want defaults %+v", got, want)
	}
}

func TestLoadFieldPriority(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	proj := filepath.Join(home, "proj")
	app := filepath.Join(proj, "app")
	mustMkdir(t, app)
	writeConfig(t, base, "keep_versions: 7\n")
	writeConfig(t, home, strings.Join([]string{
		"log_level: warn",
		"keep_versions: 2",
		"cache_dir: user-cache",
		"logs_dir: user-logs",
		"images_dir: user-images",
	}, "\n"))
	writeConfig(t, proj, strings.Join([]string{
		"images_dir: far-images",
		"log_level: debug",
		"cache_dir: proj-cache",
		"logs_dir: proj-logs",
		"sessions_dir: far-sessions",
	}, "\n"))
	writeConfig(t, app, "images_dir: near-images\n")

	level := "error"
	got, err := Load(app, home, base, "", Patch{LogLevel: &level})
	if err != nil {
		t.Fatal(err)
	}
	if got.ImagesDir != filepath.Join(app, "near-images") {
		t.Fatalf("images = %q", got.ImagesDir)
	}
	if got.SessionsDir != filepath.Join(proj, "far-sessions") {
		t.Fatalf("sessions = %q", got.SessionsDir)
	}
	if got.CacheDir != filepath.Join(proj, "proj-cache") {
		t.Fatalf("cache = %q", got.CacheDir)
	}
	if got.LogsDir != filepath.Join(proj, "proj-logs") {
		t.Fatalf("logs = %q", got.LogsDir)
	}
	if got.LogLevel != "error" {
		t.Fatalf("log level = %q", got.LogLevel)
	}
	if got.KeepVersions != 2 {
		t.Fatalf("keep versions = %d", got.KeepVersions)
	}
	if got.CacheEnabled || got.SessionMaxAge != DefaultSessionMaxAge {
		t.Fatalf("untouched fields changed: %+v", got)
	}
}

func TestLoadSessionAutocloseStaleLayers(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	proj := filepath.Join(home, "proj")
	mustMkdir(t, proj)
	writeConfig(t, home, "session_autoclose_stale: true\n")
	// A project file that omits the key keeps the user value.
	writeConfig(t, proj, "log_level: warn\n")
	got, err := Load(proj, home, base, "", Patch{})
	if err != nil {
		t.Fatal(err)
	}
	if !got.SessionAutocloseStale {
		t.Fatalf("user file value lost: %+v", got)
	}
	// The nearer project file flips it back off.
	writeConfig(t, proj, "session_autoclose_stale: false\n")
	got, err = Load(proj, home, base, "", Patch{})
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionAutocloseStale {
		t.Fatalf("project file did not override: %+v", got)
	}
	// The command line wins over both.
	on := true
	got, err = Load(proj, home, base, "", Patch{SessionAutocloseStale: &on})
	if err != nil {
		t.Fatal(err)
	}
	if !got.SessionAutocloseStale {
		t.Fatalf("command line did not override: %+v", got)
	}
}

func TestLoadExplicitZeroOverridesLowerValue(t *testing.T) {
	home := t.TempDir()
	proj := filepath.Join(home, "proj")
	mustMkdir(t, proj)
	writeConfig(t, home, strings.Join([]string{
		"keep_versions: 4",
		"cache_enabled: true",
		"session_max_age: 48h",
		"max_age: 10h",
	}, "\n"))
	writeConfig(t, proj, strings.Join([]string{
		"keep_versions: 0",
		"cache_enabled: false",
		"session_max_age: 0s",
		"max_age: 0s",
	}, "\n"))

	got, err := Load(proj, home, home, "", Patch{})
	if err != nil {
		t.Fatal(err)
	}
	if got.KeepVersions != 0 || got.CacheEnabled || got.SessionMaxAge != 0 || got.MaxAge != 0 {
		t.Fatalf("explicit zero did not override: %+v", got)
	}
	if got.SessionMaxAge == DefaultSessionMaxAge {
		t.Fatal("explicit zero session max age collapsed into the default")
	}
}

func TestLoadCommandLineZeroOverridesFile(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, "keep_versions: 4\ncache_enabled: true\n")
	zero := 0
	off := false
	got, err := Load(home, home, home, "", Patch{KeepVersions: &zero, CacheEnabled: &off})
	if err != nil {
		t.Fatal(err)
	}
	if got.KeepVersions != 0 || got.CacheEnabled {
		t.Fatalf("command line zero did not override: %+v", got)
	}
}

func TestLoadRelativePaths(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	far := filepath.Join(base, "work")
	near := filepath.Join(far, "proj")
	cwd := filepath.Join(base, "cwd")
	mustMkdir(t, home, near, cwd)
	writeConfig(t, far, "images_dir: ../sib\n")
	writeConfig(t, near, strings.Join([]string{
		"sessions_dir: ~/sessions",
		"cache_dir: /var/tiler/foo/../cache",
		"logs_dir: ~/logs",
	}, "\n"))
	t.Chdir(cwd)

	got, err := Load(near, home, base, "", Patch{})
	if err != nil {
		t.Fatal(err)
	}
	wantImages := filepath.Clean(filepath.Join(far, "..", "sib"))
	if got.ImagesDir != wantImages || strings.HasPrefix(got.ImagesDir, far+string(filepath.Separator)) {
		t.Fatalf("images = %q, want cleaned %q", got.ImagesDir, wantImages)
	}
	if got.SessionsDir != filepath.Join(home, "sessions") {
		t.Fatalf("sessions = %q", got.SessionsDir)
	}
	wantCache := filepath.Clean("/var/tiler/foo/../cache")
	if got.CacheDir != wantCache || strings.Contains(got.CacheDir, near) || strings.Contains(got.CacheDir, cwd) {
		t.Fatalf("cache = %q, want cleaned absolute %q", got.CacheDir, wantCache)
	}
	if got.LogsDir != filepath.Join(home, "logs") {
		t.Fatalf("logs = %q", got.LogsDir)
	}

	sessions := "cli-sessions"
	logs := "cli-logs"
	got, err = Load(near, home, base, "", Patch{SessionsDir: &sessions, LogsDir: &logs})
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionsDir != filepath.Join(cwd, "cli-sessions") {
		t.Fatalf("sessions = %q", got.SessionsDir)
	}
	if got.LogsDir != filepath.Join(cwd, "cli-logs") {
		t.Fatalf("logs = %q", got.LogsDir)
	}
	if got.CacheDir != wantCache {
		t.Fatalf("command line rewrote absolute cache to %q", got.CacheDir)
	}
}

func TestLoadConfigFileIsNearestExistingFile(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	sandbox := filepath.Join(home, "tmp", "tiler-test")
	work := filepath.Join(sandbox, "ws-standard")
	mustMkdir(t, work)
	user := writeConfig(t, home, "log_level: warn\n")
	project := writeConfig(t, sandbox, "images_dir: .tiler/images\n")

	got, err := Load(work, home, base, "", Patch{})
	if err != nil {
		t.Fatal(err)
	}
	if got.ConfigFile() != project {
		t.Fatalf("ConfigFile() = %q, want %q", got.ConfigFile(), project)
	}
	if got.UserFile() != user {
		t.Fatalf("UserFile() = %q, want %q", got.UserFile(), user)
	}
	if got.ImagesDir != filepath.Join(sandbox, ".tiler", "images") {
		t.Fatalf("images = %q", got.ImagesDir)
	}

	onlyUser := filepath.Join(home, "plain")
	mustMkdir(t, onlyUser)
	got, err = Load(onlyUser, home, base, "", Patch{})
	if err != nil {
		t.Fatal(err)
	}
	if got.ConfigFile() != user {
		t.Fatalf("ConfigFile() = %q, want the user file %q", got.ConfigFile(), user)
	}
}

func TestLoadWalkInsideHomeSkipsHomeProjectFile(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	app := filepath.Join(home, "app")
	mustMkdir(t, app)
	writeConfig(t, base, "keep_versions: 7\n")
	writeConfig(t, home, "log_level: warn\nkeep_versions: 3\n")
	writeConfig(t, app, "images_dir: imgs\n")

	got, err := Load(app, home, base, "", Patch{})
	if err != nil {
		t.Fatal(err)
	}
	if got.KeepVersions != 3 {
		t.Fatalf("keep versions = %d", got.KeepVersions)
	}
	if got.LogLevel != "warn" {
		t.Fatalf("log level = %q", got.LogLevel)
	}
	if got.ImagesDir != filepath.Join(app, "imgs") {
		t.Fatalf("images = %q", got.ImagesDir)
	}
}

func TestLoadWalkOutsideHomeStopsAtFilesystemRoot(t *testing.T) {
	base := t.TempDir()
	fsRoot := filepath.Join(base, "root")
	home := filepath.Join(fsRoot, "home")
	work := filepath.Join(fsRoot, "work")
	proj := filepath.Join(work, "proj")
	mustMkdir(t, home, proj)
	writeConfig(t, base, "keep_versions: 7\n")
	writeConfig(t, home, "log_level: warn\ncache_enabled: true\n")
	writeConfig(t, fsRoot, "log_level: error\ncache_dir: root-cache\n")
	writeConfig(t, work, "images_dir: work-images\n")
	writeConfig(t, proj, "images_dir: proj-images\n")

	got, err := Load(proj, home, fsRoot, "", Patch{})
	if err != nil {
		t.Fatal(err)
	}
	if got.KeepVersions != 0 {
		t.Fatalf("file above filesystem root was applied: keep versions = %d", got.KeepVersions)
	}
	if got.LogLevel != "error" {
		t.Fatalf("log level = %q", got.LogLevel)
	}
	if !got.CacheEnabled {
		t.Fatal("user cache flag was dropped")
	}
	if got.ImagesDir != filepath.Join(proj, "proj-images") {
		t.Fatalf("images = %q", got.ImagesDir)
	}
	if got.CacheDir != filepath.Join(fsRoot, "root-cache") {
		t.Fatalf("cache = %q", got.CacheDir)
	}
}

func TestLoadStartAtHomeHasNoProjectFile(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, "log_level: debug\n")
	got, err := Load(home, home, home, "", Patch{})
	if err != nil {
		t.Fatal(err)
	}
	if got.LogLevel != "debug" {
		t.Fatalf("log level = %q", got.LogLevel)
	}
	if got.ImagesDir != Defaults(home).ImagesDir {
		t.Fatalf("images = %q", got.ImagesDir)
	}
}

func TestLoadFileErrorsIncludePath(t *testing.T) {
	t.Run("unknown key", func(t *testing.T) {
		home := t.TempDir()
		path := writeConfig(t, home, "image_dir: /tmp\n")
		_, err := Load(home, home, home, "", Patch{})
		if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "image_dir") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("invalid yaml", func(t *testing.T) {
		home := t.TempDir()
		path := writeConfig(t, home, "log_level: [\n")
		_, err := Load(home, home, home, "", Patch{})
		if err == nil || !strings.Contains(err.Error(), path) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("unreadable", func(t *testing.T) {
		home := t.TempDir()
		path := writeConfig(t, home, "log_level: debug\n")
		if err := os.Chmod(path, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = os.Chmod(path, 0o644)
		})
		if _, err := os.ReadFile(path); err == nil {
			t.Skip("this user can read a mode 000 file")
		}
		_, err := Load(home, home, home, "", Patch{})
		if err == nil || !strings.Contains(err.Error(), path) {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		want string
	}{
		{name: "unknown log level", doc: "log_level: verbose\n", want: "log_level"},
		{name: "negative keep versions", doc: "keep_versions: -1\n", want: "keep_versions"},
		{name: "negative max age", doc: "max_age: -1s\n", want: "max_age"},
		{name: "negative session max age", doc: "session_max_age: -1h\n", want: "session_max_age"},
		{name: "negative split size", doc: "split_size: -1\n", want: "split_size"},
		{name: "fractional split size", doc: "split_size: 2.5G\n", want: "split_size"},
		{name: "removed split_enabled", doc: "split_enabled: true\n", want: "split_enabled"},
		{name: "removed split_mode", doc: "split_mode: blocks\n", want: "split_mode"},
		{name: "removed split_route", doc: "split_route: concat\n", want: "split_route"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			path := writeConfig(t, home, tc.doc)
			_, err := Load(home, home, home, "", Patch{})
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), path) {
				t.Fatalf("error = %v, want %s and %s", err, tc.want, path)
			}
		})
	}

	t.Run("command line", func(t *testing.T) {
		home := t.TempDir()
		path := writeConfig(t, home, "log_level: debug\n")
		level := "verbose"
		_, err := Load(home, home, home, "", Patch{LogLevel: &level})
		if err == nil || !strings.Contains(err.Error(), commandLineSource) || !strings.Contains(err.Error(), "log_level") || strings.Contains(err.Error(), path) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("nearer file replaces invalid value", func(t *testing.T) {
		home := t.TempDir()
		proj := filepath.Join(home, "proj")
		mustMkdir(t, proj)
		writeConfig(t, home, "log_level: verbose\nkeep_versions: -3\n")
		writeConfig(t, proj, "log_level: debug\nkeep_versions: 2\n")
		got, err := Load(proj, home, home, "", Patch{})
		if err != nil {
			t.Fatal(err)
		}
		if got.LogLevel != "debug" || got.KeepVersions != 2 {
			t.Fatalf("replaced invalid values did not win: %+v", got)
		}
	})

	t.Run("project file is the last setter", func(t *testing.T) {
		home := t.TempDir()
		proj := filepath.Join(home, "proj")
		mustMkdir(t, proj)
		user := writeConfig(t, home, "log_level: debug\n")
		path := writeConfig(t, proj, "log_level: verbose\n")
		_, err := Load(proj, home, home, "", Patch{})
		if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "log_level") || strings.Contains(err.Error(), user) {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestProjectPathsRejectsWalkBounds(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()

	_, err := projectPaths(" ", home, root, "")
	if err == nil || !strings.Contains(err.Error(), "start directory") {
		t.Fatalf("blank start error = %v", err)
	}
	_, err = projectPaths(root, home, "\t", "")
	if err == nil || !strings.Contains(err.Error(), "filesystem root") {
		t.Fatalf("blank root error = %v", err)
	}

	outside := t.TempDir()
	_, err = projectPaths(outside, home, root, "")
	if err == nil || !strings.Contains(err.Error(), outside) || !strings.Contains(err.Error(), root) {
		t.Fatalf("outside root error = %v", err)
	}

	if !stopWalk("/", home, filepath.Join(root, "not-root"), false) {
		t.Fatal("walk continued past the filesystem root")
	}
}

func TestLoadEmptyHome(t *testing.T) {
	_, err := Load(t.TempDir(), " ", t.TempDir(), "", Patch{})
	if err == nil {
		t.Fatal("empty home was accepted")
	}
}

func TestLoadCommandLineHomePrefixUsesInjectedHome(t *testing.T) {
	home := t.TempDir()
	dir := "~/images"
	got, err := Load(home, home, home, "", Patch{ImagesDir: &dir})
	if err != nil {
		t.Fatal(err)
	}
	if got.ImagesDir != filepath.Join(home, "images") {
		t.Fatalf("images = %q", got.ImagesDir)
	}
	if got.SessionMaxAge != 720*time.Hour {
		t.Fatalf("session max age = %s", got.SessionMaxAge)
	}
}

func TestLoadCeilingStopsUpwardWalk(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	zone := filepath.Join(home, "zone")
	sandbox := filepath.Join(zone, "sandbox")
	deep := filepath.Join(sandbox, "deep")
	mustMkdir(t, deep)
	writeConfig(t, zone, "cache_dir: marker-cache\n")

	// Without a ceiling the walk reaches zone and applies the marker.
	got, err := Load(deep, home, base, "", Patch{})
	if err != nil {
		t.Fatal(err)
	}
	if got.CacheDir != filepath.Join(zone, "marker-cache") {
		t.Fatalf("control cache = %q, want the zone marker", got.CacheDir)
	}

	// With the ceiling at sandbox, zone sits above it and is excluded, so
	// the built-in default stands.
	got, err = Load(deep, home, base, sandbox, Patch{})
	if err != nil {
		t.Fatal(err)
	}
	if got.CacheDir != filepath.Join(home, ".tiler", "cache") {
		t.Fatalf("ceiling cache = %q, want the default", got.CacheDir)
	}
}

func TestLoadCeilingIncludesOwnFile(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	sandbox := filepath.Join(home, "sandbox")
	deep := filepath.Join(sandbox, "deep")
	mustMkdir(t, deep)
	writeConfig(t, sandbox, "cache_dir: sandbox-cache\n")

	got, err := Load(deep, home, base, sandbox, Patch{})
	if err != nil {
		t.Fatal(err)
	}
	if got.CacheDir != filepath.Join(sandbox, "sandbox-cache") {
		t.Fatalf("cache = %q, want the ceiling dir's own value", got.CacheDir)
	}
}

func TestLoadCeilingIgnoredWhenNotAncestor(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	zone := filepath.Join(home, "zone")
	deep := filepath.Join(zone, "deep")
	mustMkdir(t, deep)
	writeConfig(t, zone, "cache_dir: marker-cache\n")

	// A ceiling that is not an ancestor of start is ignored, so the walk
	// still reaches zone and applies the marker.
	other := filepath.Join(base, "other")
	mustMkdir(t, other)
	got, err := Load(deep, home, base, other, Patch{})
	if err != nil {
		t.Fatal(err)
	}
	if got.CacheDir != filepath.Join(zone, "marker-cache") {
		t.Fatalf("cache = %q, want the zone marker", got.CacheDir)
	}
}

func TestLoadSplitSizeLayers(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	proj := filepath.Join(home, "proj")
	mustMkdir(t, proj)
	writeConfig(t, home, "split_size: 2G\n")
	got, err := Load(proj, home, base, "", Patch{})
	if err != nil {
		t.Fatal(err)
	}
	if got.SplitSize != 2<<30 {
		t.Fatalf("split size = %d, want 2G", got.SplitSize)
	}

	writeConfig(t, proj, "split_size: 0\n")
	got, err = Load(proj, home, base, "", Patch{})
	if err != nil {
		t.Fatal(err)
	}
	if got.SplitSize != 0 {
		t.Fatalf("project zero = %d, want it to override 2G", got.SplitSize)
	}

	zero := int64(0)
	got, err = Load(proj, home, base, "", Patch{SplitSize: &zero})
	if err != nil {
		t.Fatal(err)
	}
	if got.SplitSize != 0 {
		t.Fatalf("command zero = %d", got.SplitSize)
	}
	fourK := int64(4096)
	got, err = Load(home, home, base, "", Patch{SplitSize: &fourK})
	if err != nil {
		t.Fatal(err)
	}
	if got.SplitSize != 4096 {
		t.Fatalf("command override = %d, want 4096", got.SplitSize)
	}
}

func mustMkdir(t *testing.T, dirs ...string) {
	t.Helper()
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func writeConfig(t *testing.T, dir, body string) string {
	t.Helper()
	mustMkdir(t, dir)
	path := filepath.Join(dir, fileName)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
