package image

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/config"
	"github.com/jiangxin/tiler/internal/container"
)

type splitFixture struct {
	manifest Manifest
	bodies   [][]byte
}

func (fx splitFixture) root() ImageMeta {
	root, _ := fx.manifest.Full()
	return root
}

func (fx splitFixture) id() string { return fx.root().ID }

func blocksOnlyFixture(blocks []BlockEntry, bodies [][]byte) splitFixture {
	return splitFixture{manifest: Manifest{Images: []ImageMeta{{Kind: KindFull, Blocks: blocks}}}, bodies: bodies}
}

func blocksFixture(bodies [][]byte) splitFixture {
	var blocks []BlockEntry
	var all []byte
	for i, body := range bodies {
		sum := sha256.Sum256(body)
		blocks = append(blocks, BlockEntry{Name: fmt.Sprintf(blockNameFmt, i), Size: int64(len(body)),
			SHA256: hex.EncodeToString(sum[:]), MtimeUnix: int64(1710000000 + i)})
		all = append(all, body...)
	}
	whole := sha256.Sum256(all)
	manifest := validManifest()
	manifest.Images = []ImageMeta{{ID: ID(whole), Kind: KindFull, Size: int64(len(all)),
		SHA256: hex.EncodeToString(whole[:]), MtimeUnix: 1710000000,
		CreatedAt: time.Date(2026, 10, 16, 8, 0, 0, 0, time.UTC), Builder: "dev", Blocks: blocks}}
	manifest.Head = ID(whole)
	return splitFixture{manifest: manifest, bodies: bodies}
}

func manifestNames(fx splitFixture) []string {
	names := make([]string, len(fx.root().Blocks))
	for i, entry := range fx.root().Blocks {
		names[i] = entry.Name
	}
	return names
}

func writePayloadDir(t *testing.T, dir string, fx splitFixture) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i, entry := range fx.root().Blocks {
		path := filepath.Join(dir, entry.Name)
		if err := os.WriteFile(path, fx.bodies[i], 0o644); err != nil {
			t.Fatal(err)
		}
		stamp := time.Unix(entry.MtimeUnix, 0)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
}

func assertBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}

func assertNoTemps(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if len(entry.Name()) > 0 && entry.Name()[0] == '.' {
			t.Errorf("temp file left behind: %s", entry.Name())
		}
	}
}

func assertGone(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s still present: %v", path, err)
	}
}

func rootOf(m Manifest) ImageMeta {
	root, _ := m.Full()
	return root
}

func mustLoadManifest(t *testing.T, path string) Manifest {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := DecodeVersionManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	return manifestFromVersion(doc)
}

func buildConfig(t *testing.T) config.Config {
	t.Helper()
	return config.Defaults(t.TempDir())
}

type fakeRunner struct {
	calls [][]string
	envs  [][]string
	body  []byte
	err   error
}

func (f *fakeRunner) run(name string, env []string, args ...string) error {
	f.calls = append(f.calls, append([]string{name}, args...))
	f.envs = append(f.envs, env)
	if f.err != nil {
		return f.err
	}
	idx := 1
	if name == "mkfs.erofs" {
		idx = 3
	}
	if len(args) <= idx {
		return errors.New("fake runner: missing destination")
	}
	return os.WriteFile(args[idx], f.body, 0o644)
}

func sourceDir(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "hello.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	return src
}

func fixedNow() time.Time {
	return time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
}

func skipSmoke(string) error { return nil }

func upperDir(t *testing.T) string {
	t.Helper()
	upper := t.TempDir()
	if err := os.WriteFile(filepath.Join(upper, "new.txt"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	return upper
}

func readLeafFile(t *testing.T, repo Repo, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repo.Dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func testRepo(t *testing.T) Repo {
	t.Helper()
	images := t.TempDir()
	repo, err := OpenRepo(images, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	repo.LogsDir = logsFor(images)
	return repo
}

// logsFor is the activity root paired with one images directory.
func logsFor(imagesDir string) string { return imagesDir + ".logs" }

func manifestFor(data []byte) Manifest {
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	return fullManifest("alice/app", Format{Type: FormatSquashfs, Compression: CompressionZstd}, ImageMeta{
		ID: digest, Kind: KindFull, Size: int64(len(data)), SHA256: digest,
		MtimeUnix: 1710000000, CreatedAt: time.Date(2026, 10, 16, 8, 0, 0, 0, time.UTC), Builder: "dev",
	})
}

func indexLine(key, rel string) string { return key + " " + rel + "\n" }

func readIndex(path string) ([]indexRow, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rows []indexRow
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, errors.New("malformed index")
		}
		rows = append(rows, indexRow{key: fields[0], rel: fields[1]})
	}
	return rows, nil
}

func matchIndex(rows []indexRow, ref string, prefix bool) []indexRow {
	var matches []indexRow
	for _, row := range rows {
		if row.key == ref || (prefix && strings.HasPrefix(row.key, ref)) {
			matches = append(matches, row)
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].rel < matches[j].rel })
	return matches
}

type dmgRunner struct {
	body               []byte
	err                error
	calls              [][]string
	afterConvertShadow []byte
}

func (d *dmgRunner) run(name string, _ []string, args ...string) error {
	d.calls = append(d.calls, append([]string{name}, args...))
	if d.body != nil {
		if err := os.WriteFile(dmgArgOut(args), d.body, 0o644); err != nil {
			return err
		}
	}
	if d.err == nil && d.afterConvertShadow != nil {
		if shadow := dmgArgShadow(args); shadow != "" {
			if err := os.WriteFile(shadow, d.afterConvertShadow, 0o644); err != nil {
				return err
			}
		}
	}
	return d.err
}

func dmgArgOut(args []string) string {
	for i, arg := range args {
		if arg == "-o" && i+1 < len(args) {
			return args[i+1]
		}
	}
	if len(args) == 0 {
		return ""
	}
	return args[len(args)-1]
}

func dmgArgShadow(args []string) string {
	for i, arg := range args {
		if arg == "-shadow" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func writeShadow(t *testing.T, sessions, name string, seq int, body []byte) string {
	t.Helper()
	shadow, err := container.DMGShadowPath(sessions, name, seq)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(shadow), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shadow, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return shadow
}
