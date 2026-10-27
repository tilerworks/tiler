package image

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jiangxin/tiler/internal/container"
)

func TestActivePayloadPathFollowsFormat(t *testing.T) {
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
		meta := validMeta()
		meta.Format.Type = tc.typ
		got, err := activePayloadPath(repo, meta)
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
	meta := validMeta()
	meta.Format = Format{Type: FormatDMG, Compression: "ulfo"}
	if len(meta.Chain) != 0 {
		t.Fatal("fixture chain is not empty")
	}
	if err := os.MkdirAll(repo.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(repo.Dir, ImageDMG)
	if err := os.WriteFile(payload, []byte("dmg"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Store(repo.MetaPath(), meta); err != nil {
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
	meta := seedChain(t, repo, 0)
	got, err := ResolveMount(cfg, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(repo.Dir, "image.sqsh")
	if meta.Format.Type != FormatSquashfs || len(got.Members) != 1 || got.Members[0].Path != want {
		t.Fatalf("path = %s format = %s", got.Members[0].Path, meta.Format.Type)
	}
	if _, err := os.Stat(repo.ImagePath()); err != nil {
		t.Fatal(err)
	}
}

func TestValidateAcceptsDMGCompressionWithoutRegistry(t *testing.T) {
	for _, compression := range []string{"ulfo", "ulmo", "udro"} {
		meta := validMeta()
		meta.Format = Format{Type: FormatDMG, Compression: compression}
		if err := meta.Validate(); err != nil {
			t.Errorf("dmg/%s: %v", compression, err)
		}
	}
	meta := validMeta()
	meta.Format = Format{Type: FormatDMG, Compression: "zstd"}
	err := meta.Validate()
	if err == nil || !strings.Contains(err.Error(), `format.compression: dmg does not allow "zstd"`) {
		t.Fatalf("zstd: %v", err)
	}
	meta.Format.Compression = ""
	err = meta.Validate()
	if err == nil || !strings.Contains(err.Error(), `format.compression: dmg does not allow ""`) {
		t.Fatalf("empty: %v", err)
	}
}

func TestEmptyChainDMGSurvivesPreparePublish(t *testing.T) {
	repo := testRepo(t)
	body := []byte("dmg-bytes")
	meta := seedDMGActive(t, repo, body)

	got, exists, err := preparePublish(repo, nil, hooks{}, "")
	if err != nil {
		t.Fatal(err)
	}
	root, err := got.Full()
	if err != nil {
		t.Fatal(err)
	}
	if !exists || root.ID != meta.ID || got.Format.Type != FormatDMG {
		t.Fatalf("preparePublish = %+v exists=%v", got, exists)
	}
	if err := repairTornActive(repo, hooks{}, ""); err != nil {
		t.Fatal(err)
	}
	assertDMGStillActive(t, repo, body, meta)

	if payloadRoot(repo.Dir, manifestFromMeta(meta)) != filepath.Join(repo.Dir, ImageDMG) {
		t.Fatalf("root = %s", payloadRoot(repo.Dir, manifestFromMeta(meta)))
	}
	holdPath := holdPayloadPath("hold", meta)
	if holdPath != filepath.Join("hold", ImageDMG) {
		t.Fatalf("hold = %s", holdPath)
	}
	ok, err := payloadMatchesMeta(repo, meta)
	if err != nil || !ok {
		t.Fatalf("dmg match = %v %v", ok, err)
	}
	squash := meta
	squash.Format.Type = FormatSquashfs
	ok, err = payloadMatchesMeta(repo, squash)
	if err != nil || ok {
		t.Fatalf("squashfs matched image.dmg: %v %v", ok, err)
	}
}

func TestRepairRestoresEmptyChainDMGFromHold(t *testing.T) {
	repo := testRepo(t)
	body := []byte("held-dmg")
	meta := metaFor(body)
	meta.Format = Format{Type: FormatDMG, Compression: "ulfo"}
	if len(meta.Chain) != 0 {
		t.Fatal("fixture chain is not empty")
	}
	if err := os.MkdirAll(repo.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Store(repo.MetaPath(), meta); err != nil {
		t.Fatal(err)
	}
	hold, err := repo.HoldPath(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(hold, 0o755); err != nil {
		t.Fatal(err)
	}
	payload := holdPayloadPath(hold, meta)
	if filepath.Base(payload) != ImageDMG {
		t.Fatalf("held payload = %s", payload)
	}
	if err := os.WriteFile(payload, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := repairTornActive(repo, hooks{}, ""); err != nil {
		t.Fatal(err)
	}
	assertDMGStillActive(t, repo, body, meta)
	assertGone(t, hold)
}

func TestPreparePublishKeepsSquashfsOnImageSqsh(t *testing.T) {
	repo := testRepo(t)
	body := []byte("sqsh-bytes")
	meta := seedActive(t, repo, body, false)
	if meta.Format.Type != FormatSquashfs || len(meta.Chain) != 0 {
		t.Fatalf("fixture = %+v", meta.Format)
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
	if !exists || root.SHA256 != meta.SHA256 {
		t.Fatalf("preparePublish = %+v exists=%v", got, exists)
	}
	assertBytes(t, repo.ImagePath(), body)
	if _, err := os.Stat(filepath.Join(repo.Dir, ImageDMG)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("repair created image.dmg")
	}
	if payloadRoot(repo.Dir, manifestFromMeta(meta)) != repo.ImagePath() {
		t.Fatalf("root = %s", payloadRoot(repo.Dir, manifestFromMeta(meta)))
	}
	if holdPayloadPath("hold", meta) != filepath.Join("hold", ImageFile) {
		t.Fatalf("hold = %s", holdPayloadPath("hold", meta))
	}
	erofs := meta
	erofs.Format.Type = FormatErofs
	if payloadRoot(repo.Dir, manifestFromMeta(erofs)) != filepath.Join(repo.Dir, ImageFile) {
		t.Fatalf("erofs root = %s", payloadRoot(repo.Dir, manifestFromMeta(erofs)))
	}
	ok, err := payloadMatchesMeta(repo, meta)
	if err != nil || !ok {
		t.Fatalf("squashfs match = %v %v", ok, err)
	}
}

func seedDMGActive(t *testing.T, repo Repo, body []byte) Meta {
	t.Helper()
	meta := metaFor(body)
	meta.Format = Format{Type: FormatDMG, Compression: "ulfo"}
	if len(meta.Chain) != 0 {
		t.Fatal("fixture chain is not empty")
	}
	if err := os.MkdirAll(repo.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo.Dir, ImageDMG), body, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := StoreManifest(repo.MetaPath(), manifestFromMeta(meta)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(repo.ImagePath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("fixture must not include image.sqsh")
	}
	return meta
}

func assertDMGStillActive(t *testing.T, repo Repo, body []byte, meta Meta) {
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
	if !state.HasActive || state.Active.ID != meta.ID || state.Active.Format.Type != FormatDMG {
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
