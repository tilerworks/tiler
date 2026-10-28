package image

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jiangxin/tiler/internal/container"
)

// singleFormatManifest trims validManifest to its full root alone and
// records format on the manifest.
func singleFormatManifest(format Format) Manifest {
	manifest := validManifest()
	manifest.Images = manifest.Images[:1]
	manifest.Head = manifest.Images[0].ID
	manifest.Format = format
	return manifest
}

func TestManifestPayloadPathFollowsFormat(t *testing.T) {
	repo, err := OpenRepo("/var/lib/images", "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	if ImageFile != "image.sqsh" {
		t.Fatalf("ImageFile = %q", ImageFile)
	}
	root := repo.Dir
	cases := []struct {
		typ  string
		want string
	}{
		{typ: FormatDMG, want: filepath.Join(root, "image.dmg")},
		{typ: FormatSquashfs, want: filepath.Join(root, "image.sqsh")},
		{typ: FormatErofs, want: filepath.Join(root, "image.sqsh")},
	}
	for _, tc := range cases {
		manifest := singleFormatManifest(
			Format{Type: tc.typ, Compression: validManifest().Format.Compression})
		if tc.typ == FormatDMG {
			manifest.Format.Compression = "ulfo"
		}
		got, err := manifestPayloadPath(repo, manifest)
		if err != nil {
			t.Fatalf("%s: %v", tc.typ, err)
		}
		if got != tc.want {
			t.Fatalf("%s path = %s, want %s", tc.typ, got, tc.want)
		}
	}
	if repo.ImagePath() != filepath.Join(root, ImageFile) {
		t.Fatalf("ImagePath = %s", repo.ImagePath())
	}
}

func TestResolveMountDMGUsesImageDMG(t *testing.T) {
	cfg := buildConfig(t)
	repo := mustRepo(t, cfg)
	manifest := singleFormatManifest(Format{Type: FormatDMG, Compression: "ulfo"})
	if len(manifest.Images) != 1 {
		t.Fatal("fixture chain is not empty")
	}
	if err := os.MkdirAll(repo.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(repo.Dir, ImageDMG)
	if err := os.WriteFile(payload, []byte("dmg"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := StoreManifest(repo.MetaPath(), manifest); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveMount(cfg, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	if got.Format != FormatDMG || len(got.Members) != 1 || got.Members[0].Path != payload {
		t.Fatalf("target = %+v", got)
	}
}

func TestResolveMountSquashfsKeepsImageSqsh(t *testing.T) {
	cfg := buildConfig(t)
	repo := mustRepo(t, cfg)
	manifest := seedChain(t, repo, 0)
	got, err := ResolveMount(cfg, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(repo.Dir, "image.sqsh")
	if manifest.Format.Type != FormatSquashfs || len(got.Members) != 1 || got.Members[0].Path != want {
		t.Fatalf("path = %s format = %s", got.Members[0].Path, manifest.Format.Type)
	}
	if _, err := os.Stat(repo.ImagePath()); err != nil {
		t.Fatal(err)
	}
}

func TestValidateAcceptsDMGCompressionWithoutRegistry(t *testing.T) {
	for _, compression := range []string{"ulfo", "ulmo", "udro"} {
		meta := validMeta()
		meta.Format = Format{Type: FormatDMG, Compression: compression}
		if err := meta.validate(); err != nil {
			t.Errorf("dmg/%s: %v", compression, err)
		}
	}
	meta := validMeta()
	meta.Format = Format{Type: FormatDMG, Compression: "zstd"}
	err := meta.validate()
	if err == nil || !strings.Contains(err.Error(), `format.compression: dmg does not allow "zstd"`) {
		t.Fatalf("zstd: %v", err)
	}
	meta.Format.Compression = ""
	err = meta.validate()
	if err == nil || !strings.Contains(err.Error(), `format.compression: dmg does not allow ""`) {
		t.Fatalf("empty: %v", err)
	}
}

func TestEmptyChainDMGSurvivesPreparePublish(t *testing.T) {
	repo := testRepo(t)
	body := []byte("dmg-bytes")
	manifest := seedDMGActive(t, repo, body)

	got, exists, err := preparePublish(repo, nil, hooks{}, "")
	if err != nil {
		t.Fatal(err)
	}
	root, err := got.Full()
	if err != nil {
		t.Fatal(err)
	}
	if !exists || root.ID != rootID(manifest) || got.Format.Type != FormatDMG {
		t.Fatalf("preparePublish = %+v exists=%v", got, exists)
	}
	if err := repairTornActive(repo, hooks{}, ""); err != nil {
		t.Fatal(err)
	}
	assertDMGStillActive(t, repo, body, manifest)

	if payloadRoot(repo.Dir, manifest) != filepath.Join(repo.Dir, ImageDMG) {
		t.Fatalf("root = %s", payloadRoot(repo.Dir, manifest))
	}
	holdPath, err := holdPayloadPath("hold", manifest)
	if err != nil {
		t.Fatal(err)
	}
	if holdPath != filepath.Join("hold", ImageDMG) {
		t.Fatalf("hold = %s", holdPath)
	}
	ok, err := payloadMatchesMeta(repo, manifest)
	if err != nil || !ok {
		t.Fatalf("dmg match = %v %v", ok, err)
	}
	squash := manifest
	squash.Format.Type = FormatSquashfs
	squash.Format.Compression = CompressionZstd
	ok, err = payloadMatchesMeta(repo, squash)
	if err != nil || ok {
		t.Fatalf("squashfs matched image.dmg: %v %v", ok, err)
	}
}

func TestRepairRestoresEmptyChainDMGFromHold(t *testing.T) {
	repo := testRepo(t)
	body := []byte("held-dmg")
	manifest := manifestFor(body)
	manifest.Format = Format{Type: FormatDMG, Compression: "ulfo"}
	if len(manifest.Images) != 1 {
		t.Fatal("fixture chain is not empty")
	}
	if err := os.MkdirAll(repo.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := StoreManifest(repo.MetaPath(), manifest); err != nil {
		t.Fatal(err)
	}
	hold, err := repo.HoldPath(rootID(manifest))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(hold, 0o755); err != nil {
		t.Fatal(err)
	}
	payload, err := holdPayloadPath(hold, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(payload) != ImageDMG {
		t.Fatalf("held payload = %s", payload)
	}
	if err := os.WriteFile(payload, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := repairTornActive(repo, hooks{}, ""); err != nil {
		t.Fatal(err)
	}
	assertDMGStillActive(t, repo, body, manifest)
	assertGone(t, hold)
}

func TestPreparePublishKeepsSquashfsOnImageSqsh(t *testing.T) {
	repo := testRepo(t)
	body := []byte("sqsh-bytes")
	manifest := seedActive(t, repo, body, false)
	if manifest.Format.Type != FormatSquashfs || len(manifest.Images) != 1 {
		t.Fatalf("fixture = %+v", manifest.Format)
	}
	if _, err := os.Stat(filepath.Join(repo.Dir, ImageDMG)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("fixture included image.dmg")
	}
	got, exists, err := preparePublish(repo, nil, hooks{}, "")
	if err != nil {
		t.Fatal(err)
	}
	root, err := got.Full()
	if err != nil {
		t.Fatal(err)
	}
	if !exists || root.SHA256 != rootOf(manifest).SHA256 {
		t.Fatalf("preparePublish = %+v exists=%v", got, exists)
	}
	assertBytes(t, repo.ImagePath(), body)
	if _, err := os.Stat(filepath.Join(repo.Dir, ImageDMG)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("repair created image.dmg")
	}
	if payloadRoot(repo.Dir, manifest) != repo.ImagePath() {
		t.Fatalf("root = %s", payloadRoot(repo.Dir, manifest))
	}
	holdPath, err := holdPayloadPath("hold", manifest)
	if err != nil {
		t.Fatal(err)
	}
	if holdPath != filepath.Join("hold", ImageFile) {
		t.Fatalf("hold = %s", holdPath)
	}
	erofs := manifest
	erofs.Format.Type = FormatErofs
	if payloadRoot(repo.Dir, erofs) != filepath.Join(repo.Dir, ImageFile) {
		t.Fatalf("erofs root = %s", payloadRoot(repo.Dir, erofs))
	}
	ok, err := payloadMatchesMeta(repo, manifest)
	if err != nil || !ok {
		t.Fatalf("squashfs match = %v %v", ok, err)
	}
}

func seedDMGActive(t *testing.T, repo Repo, body []byte) Manifest {
	t.Helper()
	manifest := manifestFor(body)
	manifest.Format = Format{Type: FormatDMG, Compression: "ulfo"}
	if len(manifest.Images) != 1 {
		t.Fatal("fixture chain is not empty")
	}
	if err := os.MkdirAll(repo.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo.Dir, ImageDMG), body, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := StoreManifest(repo.MetaPath(), manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(repo.ImagePath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("fixture must not include image.sqsh")
	}
	return manifest
}

func assertDMGStillActive(t *testing.T, repo Repo, body []byte, manifest Manifest) {
	t.Helper()
	assertBytes(t, filepath.Join(repo.Dir, ImageDMG), body)
	if _, err := os.Stat(repo.ImagePath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("image.sqsh appeared")
	}
	if _, err := os.Stat(repo.MetaPath()); err != nil {
		t.Fatalf("meta: %v", err)
	}
	state, err := Inspect(repo)
	if err != nil {
		t.Fatal(err)
	}
	if !state.HasActive || rootID(state.Active) != rootID(manifest) ||
		state.Active.Format.Type != FormatDMG {
		t.Fatalf("show state = %+v", state)
	}
}

func TestBuildFullRejectsUnregisteredDMG(t *testing.T) {
	if _, err := container.Lookup(FormatDMG); err == nil {
		t.Skip("dmg is registered on this OS")
	}
	runner := &fakeRunner{body: []byte("nope")}
	_, err := BuildFull(FullRequest{
		Config: buildConfig(t), Name: "alice/app", SrcDir: sourceDir(t),
		Builder: "test", Format: FormatDMG, Compression: "ulfo",
		Run: runner.run,
	})
	if err == nil || !strings.Contains(err.Error(), "the format mounts only on macOS") {
		t.Fatalf("err = %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("packer ran: %v", runner.calls)
	}
}
