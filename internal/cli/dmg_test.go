package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/container"
	"github.com/jiangxin/tiler/internal/image"
)

func TestImagesShowDMGFromMeta(t *testing.T) {
	home, _ := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	for _, compression := range []string{"ulfo", "ulmo", "udro"} {
		t.Run(compression, func(t *testing.T) {
			writeDMGFixture(t, imagesDir, compression)
			out := mustShow(t, lib, "alice/app")
			for _, want := range []string{
				"  format      dmg/" + compression + "\n",
				"  form        single\n",
				"  chain       1\n",
			} {
				if !strings.Contains(out, want) {
					t.Fatalf("show missing %q:\n%s", want, out)
				}
			}
		})
	}
}

func TestImagesShowDMGSurvivesPublishRepair(t *testing.T) {
	home, _ := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	body := []byte("dmg!")
	meta := writeHashedDMGFixture(t, imagesDir, body)
	repo, err := image.OpenRepo(imagesDir, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Publish(body, meta, nil); err != nil {
		t.Fatal(err)
	}
	sqsh := filepath.Join(repo.Dir, image.ImageFile)
	if _, err := os.Stat(sqsh); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("image.sqsh after repair: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo.Dir, image.ImageDMG)); err != nil {
		t.Fatal(err)
	}
	out := mustShow(t, lib, "alice/app")
	for _, want := range []string{
		"  format      dmg/ulfo\n",
		"  form        single\n",
		"  chain       1\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("show missing %q:\n%s", want, out)
		}
	}
}

func TestMountAndBuildRejectUnregisteredDMG(t *testing.T) {
	if _, err := container.Lookup(image.FormatDMG); err == nil {
		t.Skip("dmg is registered on this OS")
	}
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	writeDMGFixture(t, imagesDir, "ulfo")
	withMountSeams(t, nil)
	view := filepath.Join(work, "view")
	_, stderr, _, err := runCmd(t, append(lib, "mount", "alice/app", view)...)
	if err == nil || !strings.Contains(err.Error()+"\n"+stderr, "the format mounts only on macOS") {
		t.Fatalf("mount err = %v\n%s", err, stderr)
	}

	withBuildSeams(t, nil)
	src := filepath.Join(work, "src")
	writeTestFile(t, filepath.Join(src, "f.txt"), "x")
	_, stderr, _, err = runCmd(t, append(lib, "build", "--format", "dmg", "--image", "bob/app", src)...)
	if err == nil || !strings.Contains(err.Error()+"\n"+stderr, "the format mounts only on macOS") {
		t.Fatalf("build err = %v\n%s", err, stderr)
	}
}

func writeDMGFixture(t *testing.T, imagesDir, compression string) {
	t.Helper()
	dir := filepath.Join(imagesDir, "alice", "app")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("dmg!")
	path := filepath.Join(dir, image.ImageDMG)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	const sum = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	meta := image.Meta{
		ID:        sum[:16],
		Owner:     "alice",
		Repo:      "app",
		Kind:      image.KindFull,
		Format:    image.Format{Type: image.FormatDMG, Compression: compression},
		Size:      info.Size(),
		SHA256:    sum,
		MtimeUnix: info.ModTime().Unix(),
		CreatedAt: time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC),
		Builder:   "test",
	}
	if len(meta.Chain) != 0 {
		t.Fatal("fixture chain is not empty")
	}
	if err := image.Store(filepath.Join(dir, "meta.yaml"), meta); err != nil {
		t.Fatal(err)
	}
}

func writeHashedDMGFixture(t *testing.T, imagesDir string, body []byte) image.Meta {
	t.Helper()
	dir := filepath.Join(imagesDir, "alice", "app")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, image.ImageDMG)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	meta := image.Meta{
		ID:        image.ID(sum),
		Owner:     "alice",
		Repo:      "app",
		Kind:      image.KindFull,
		Format:    image.Format{Type: image.FormatDMG, Compression: "ulfo"},
		Size:      info.Size(),
		SHA256:    hex.EncodeToString(sum[:]),
		MtimeUnix: info.ModTime().Unix(),
		CreatedAt: time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC),
		Builder:   "test",
	}
	if len(meta.Chain) != 0 {
		t.Fatal("fixture chain is not empty")
	}
	if err := image.Store(filepath.Join(dir, "meta.yaml"), meta); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, image.ImageFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("fixture must not include image.sqsh")
	}
	return meta
}
