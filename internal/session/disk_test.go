package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/container"
	"github.com/jiangxin/tiler/internal/image"
)

// infoVolume is one mounted volume in an hdiutil info fixture.
type infoVolume struct {
	image  string
	shadow string
	point  string
}

// hdiutilInfoPlist builds an hdiutil info -plist document. A disk
// entity without a mount point is included so the parser has to
// ignore it.
func hdiutilInfoPlist(volumes []infoVolume) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">`)
	b.WriteString(`<plist version="1.0"><dict><key>images</key><array>`)
	for _, vol := range volumes {
		b.WriteString(`<dict>`)
		writePlistString(&b, "image-path", vol.image)
		if vol.shadow != "" {
			writePlistString(&b, "shadow-path", vol.shadow)
		}
		b.WriteString(`<key>system-entities</key><array>`)
		b.WriteString(`<dict><key>dev-entry</key><string>/dev/disk2</string></dict><dict>`)
		writePlistString(&b, "mount-point", vol.point)
		b.WriteString(`</dict></array></dict>`)
	}
	b.WriteString(`</array></dict></plist>`)
	return []byte(b.String())
}

func writePlistString(b *strings.Builder, key, value string) {
	b.WriteString("<key>")
	_ = xml.EscapeText(b, []byte(key))
	b.WriteString("</key><string>")
	_ = xml.EscapeText(b, []byte(value))
	b.WriteString("</string>")
}

func diskImagesFromPlist(t *testing.T, doc []byte) []DiskImage {
	t.Helper()
	parsed, err := container.ParseHdiutilInfo(doc)
	if err != nil {
		t.Fatal(err)
	}
	images := make([]DiskImage, len(parsed))
	for i, item := range parsed {
		images[i] = DiskImage{Point: item.MountPoint, Image: item.ImagePath, Shadow: item.ShadowPath}
	}
	return images
}

func TestDiskInfoReconcilesHealthyStaleAndOrphan(t *testing.T) {
	const (
		imagesRoot  = "/images"
		sessions    = "/sessions"
		devPoint    = "/ws/dev"
		gonePoint   = "/ws/gone"
		orphanPoint = "/ws/orphan"
	)
	devImage := filepath.Join(imagesRoot, "alice", "app", "image.dmg")
	devShadow := filepath.Join(sessions, "dev", "runtime", "m1", "upper.shadow")
	orphanImage := filepath.Join(imagesRoot, "carol", "tool", "image.dmg")
	orphanShadow := filepath.Join(sessions, "orphan", "runtime", "m1", "upper.shadow")
	doc := hdiutilInfoPlist([]infoVolume{
		{image: devImage, shadow: devShadow, point: devPoint},
		{image: orphanImage, shadow: orphanShadow, point: orphanPoint},
		{image: "/tmp/foreign.dmg", point: "/Volumes/Foreign"},
	})
	images := diskImagesFromPlist(t, doc)
	if len(images) != 3 {
		t.Fatalf("parsed volumes = %d, want healthy, orphan, and foreign", len(images))
	}

	e := newEngine(t.TempDir())
	e.SessionsDir = sessions
	e.ImagesDir = imagesRoot
	e.LoadDiskImages = func() ([]DiskImage, error) { return images, nil }
	seedMounted(t, e, "dev", devPoint)
	seedMounted(t, e, "gone", gonePoint)

	overview, err := e.Overview("dev")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]View{}
	for _, view := range overview.Sessions {
		byName[view.Name] = view
	}
	dev := byName["dev"]
	if len(dev.Actions) != 1 || dev.Actions[0].Health != HealthHealthy {
		t.Fatalf("dev = %+v, want one healthy mount", dev.Actions)
	}
	gone := byName["gone"]
	if len(gone.Actions) != 1 || gone.Actions[0].Health != HealthStale {
		t.Fatalf("gone = %+v, want one stale mount", gone.Actions)
	}
	if len(overview.Orphans) != 1 {
		t.Fatalf("orphans = %+v, want the unclaimed library volume", overview.Orphans)
	}
	orphan := overview.Orphans[0]
	if orphan.Point != orphanPoint || orphan.FSType != "dmg" || orphan.RuntimeDir != filepath.Dir(orphanShadow) {
		t.Fatalf("orphan = %+v", orphan)
	}

	// A path inside the mount counts as that mount, and the matching
	// volume is not stale.
	inside, err := e.SessionForPath(devPoint + "/src/main.go")
	if err != nil {
		t.Fatal(err)
	}
	if inside.Record.Name != "dev" || inside.Mountpoint != devPoint || inside.Stale {
		t.Fatalf("inside = %+v, want the healthy dev mount", inside)
	}
	stale, err := e.SessionForPath(gonePoint + "/src/main.go")
	if err != nil {
		t.Fatal(err)
	}
	if stale.Record.Name != "gone" || !stale.Stale {
		t.Fatalf("gone path = %+v, want the stale record", stale)
	}
}

func TestDiskMatchReadOnlyOmitsShadow(t *testing.T) {
	action := Action{
		Type: TypeMount, Mountpoint: "/ws", Image: "/img.dmg",
		Mode: ModeRO, Status: StatusMounted,
	}
	e := &Engine{SessionsDir: "/sessions"}
	images := []DiskImage{{Point: "/ws", Image: "/img.dmg"}}
	if !e.diskMatch("dev", action, images) {
		t.Fatal("read-only volume without a shadow did not match")
	}
	images[0].Shadow = "/sessions/dev/runtime/m1/upper.shadow"
	if e.diskMatch("dev", action, images) {
		t.Fatal("read-only volume matched a shadow file")
	}
}

func TestOpenReplayUsesOriginalShadow(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(dir)
	e.SessionsDir = dir
	e.ImagesDir = filepath.Join(dir, "images")
	e.Clean = func(string) error { return nil }
	view := filepath.Join(dir, "view")
	seedMounted(t, e, "dev", view)
	shadow := filepath.Join(dir, "dev", "runtime", "m1", "upper.shadow")
	if err := os.MkdirAll(filepath.Dir(shadow), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shadow, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Info does not list the mount, so open replays the original
	// action. The shadow file already at that path stays the only one.
	e.LoadDiskImages = func() ([]DiskImage, error) { return nil, nil }
	mounts := 0
	mount := func(action Action) (Result, error) {
		mounts++
		if action.Seq != 1 || action.RuntimeID != "m1" {
			t.Fatalf("replay action = %+v, want the original m1", action)
		}
		replay := filepath.Join(dir, "dev", "runtime", action.RuntimeID, "upper.shadow")
		if replay != shadow {
			t.Fatalf("replay shadow = %s, want %s", replay, shadow)
		}
		if _, err := os.Stat(replay); err != nil {
			if writeErr := os.WriteFile(replay, []byte("second"), 0o644); writeErr != nil {
				t.Fatal(writeErr)
			}
		}
		return Result{Mountpoint: action.Mountpoint, Upper: replay}, nil
	}
	if err := e.Open("dev", mount); err != nil {
		t.Fatal(err)
	}
	if mounts != 1 {
		t.Fatalf("mounts = %d, want one replay", mounts)
	}
	body, err := os.ReadFile(shadow)
	if err != nil || string(body) != "keep" {
		t.Fatalf("shadow = %q, %v", body, err)
	}
	if n := countShadows(t, dir); n != 1 {
		t.Fatalf("upper.shadow files = %d, want 1", n)
	}

	image := filepath.Join(e.ImagesDir, "alice", "app", "image.dmg")
	e.LoadDiskImages = func() ([]DiskImage, error) {
		return []DiskImage{{Point: view, Image: image, Shadow: shadow}}, nil
	}
	if err := e.Open("dev", mount); err != nil {
		t.Fatal(err)
	}
	if mounts != 1 {
		t.Fatalf("mounts = %d, want the live volume to be skipped", mounts)
	}
	if n := countShadows(t, dir); n != 1 {
		t.Fatalf("upper.shadow files = %d after the second open, want 1", n)
	}
}

func countShadows(t *testing.T, root string) int {
	t.Helper()
	n := 0
	err := filepath.WalkDir(root, func(_ string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && entry.Name() == "upper.shadow" {
			n++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// symlinkDir returns a directory path that contains a symlink and the
// EvalSymlinks spelling of that same directory. They differ, which is
// the /tmp versus /private/tmp shape hdiutil produces.
func symlinkDir(t *testing.T, name string) (link, resolved string) {
	t.Helper()
	base := t.TempDir()
	concrete := filepath.Join(base, "concrete")
	if err := os.Mkdir(concrete, 0o755); err != nil {
		t.Fatal(err)
	}
	link = filepath.Join(base, name)
	if err := os.Symlink(concrete, link); err != nil {
		t.Fatal(err)
	}
	var err error
	resolved, err = filepath.EvalSymlinks(link)
	if err != nil {
		t.Fatal(err)
	}
	if resolved == link {
		t.Fatal("symlink fixture resolved to the link path")
	}
	return link, resolved
}

func TestDiskMatchResolvesMountPointNotImageOrShadow(t *testing.T) {
	linkSessions, _ := symlinkDir(t, "sessions")
	linkImages, resolvedImages := symlinkDir(t, "images")
	linkPoint, resolvedPoint := symlinkDir(t, "mp")
	linkImage := filepath.Join(linkImages, "alice", "app", "image.dmg")
	if err := os.MkdirAll(filepath.Dir(linkImage), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(linkImage, []byte("dmg"), 0o644); err != nil {
		t.Fatal(err)
	}
	resolvedImage, err := filepath.EvalSymlinks(linkImage)
	if err != nil {
		t.Fatal(err)
	}
	if resolvedImage == linkImage || !strings.HasPrefix(resolvedImage, resolvedImages) {
		t.Fatalf("image %s resolved to %s", linkImage, resolvedImage)
	}

	e := newEngine(linkSessions)
	e.SessionsDir = linkSessions
	e.ImagesDir = linkImages
	e.Clean = func(string) error { return nil }
	seedMounted(t, e, "dev", linkPoint)
	gonePoint := filepath.Join(t.TempDir(), "gone")
	seedMounted(t, e, "gone", gonePoint)

	linkShadow := filepath.Join(linkSessions, "dev", "runtime", "m1", "upper.shadow")
	if err := os.MkdirAll(filepath.Dir(linkShadow), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(linkShadow, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	resolvedShadow, err := filepath.EvalSymlinks(linkShadow)
	if err != nil || resolvedShadow == linkShadow {
		t.Fatalf("shadow %s resolved to %s, %v", linkShadow, resolvedShadow, err)
	}

	rec, err := e.Load("dev")
	if err != nil {
		t.Fatal(err)
	}
	action := rec.Actions[0]
	positive := []DiskImage{{
		Point: resolvedPoint, Image: linkImage, Shadow: linkShadow,
	}}
	if !e.diskMatch("dev", action, positive) {
		t.Fatal("resolved mount point did not match the recorded mount")
	}
	if e.diskMatch("dev", action, []DiskImage{{
		Point: resolvedPoint, Image: resolvedImage, Shadow: linkShadow,
	}}) {
		t.Fatal("image-path was symlink-resolved")
	}
	if e.diskMatch("dev", action, []DiskImage{{
		Point: resolvedPoint, Image: linkImage, Shadow: resolvedShadow,
	}}) {
		t.Fatal("shadow-path was symlink-resolved")
	}

	orphanPoint := filepath.Join(t.TempDir(), "orphan")
	orphanImage := filepath.Join(linkImages, "carol", "tool", "image.dmg")
	orphanShadow := filepath.Join(linkSessions, "orphan", "runtime", "m1", "upper.shadow")
	e.LoadDiskImages = func() ([]DiskImage, error) {
		return []DiskImage{
			{Point: resolvedPoint, Image: linkImage, Shadow: linkShadow},
			{Point: orphanPoint, Image: orphanImage, Shadow: orphanShadow},
		}, nil
	}
	overview, err := e.Overview("")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]View{}
	for _, view := range overview.Sessions {
		byName[view.Name] = view
	}
	dev := byName["dev"]
	if len(dev.Actions) != 1 || dev.Actions[0].Health != HealthHealthy {
		t.Fatalf("dev = %+v, want one healthy mount", dev.Actions)
	}
	gone := byName["gone"]
	if len(gone.Actions) != 1 || gone.Actions[0].Health != HealthStale {
		t.Fatalf("gone = %+v, want one stale mount", gone.Actions)
	}
	if len(overview.Orphans) != 1 || overview.Orphans[0].Point != orphanPoint {
		t.Fatalf("orphans = %+v, want only %s", overview.Orphans, orphanPoint)
	}

	inside := filepath.Join(linkPoint, "src", "main.go")
	if err := os.MkdirAll(filepath.Dir(inside), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inside, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hit, err := e.SessionForPath(inside)
	if err != nil {
		t.Fatal(err)
	}
	if hit.Record.Name != "dev" || hit.Mountpoint != linkPoint || hit.Stale {
		t.Fatalf("inside = %+v, want the healthy dev mount", hit)
	}
	stale, err := e.SessionForPath(filepath.Join(gonePoint, "src", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if stale.Record.Name != "gone" || !stale.Stale {
		t.Fatalf("gone path = %+v, want the stale record", stale)
	}

	mounts := 0
	if err := e.Open("dev", func(action Action) (Result, error) {
		mounts++
		return Result{Mountpoint: action.Mountpoint}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if mounts != 0 {
		t.Fatalf("mounts = %d, want the live volume to be skipped", mounts)
	}
}

func TestDiskMatchUsesAttachedImagePath(t *testing.T) {
	const sum = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	images := t.TempDir()
	repo, err := image.OpenRepo(images, "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(repo.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := image.Meta{
		ID:        sum,
		Name:      "alice/app",
		Kind:      image.KindFull,
		Format:    image.Format{Type: image.FormatDMG, Compression: "ulfo"},
		Size:      4,
		SHA256:    sum,
		MtimeUnix: 1710000000,
		CreatedAt: time.Date(2026, 10, 16, 8, 0, 0, 0, time.UTC),
		Builder:   "dev",
	}
	if err := image.Store(repo.MetaPath(), meta); err != nil {
		t.Fatal(err)
	}
	cache := t.TempDir()
	cacheFile := filepath.Join(cache, sum+image.SquashExt)
	dmg := filepath.Join(images, "alice", "app", "image.dmg")
	library := Action{
		Type: TypeMount, Image: "alice/app", Name: "alice/app",
		Mountpoint: "/ws/lib", Mode: ModeRO, Status: StatusMounted,
	}
	e := &Engine{ImagesDir: images, CacheDir: cache}
	if !e.diskMatch("dev", library, []DiskImage{{Point: library.Mountpoint, Image: dmg}}) {
		t.Fatal("cache off did not match image.dmg")
	}
	if e.diskMatch("dev", library, []DiskImage{{Point: library.Mountpoint, Image: cacheFile}}) {
		t.Fatal("cache off matched the cache copy")
	}
	e.CacheEnabled = true
	if !e.diskMatch("dev", library, []DiskImage{{Point: library.Mountpoint, Image: cacheFile}}) {
		t.Fatal("cache on did not match the cache copy attach opened")
	}
	if !e.diskMatch("dev", library, []DiskImage{{Point: library.Mountpoint, Image: dmg}}) {
		t.Fatal("cache on dropped the library image.dmg match")
	}

	dir := t.TempDir()
	t.Chdir(dir)
	const name = "payload.dmg"
	body := []byte("dmg-bytes")
	if err := os.WriteFile(name, body, 0o644); err != nil {
		t.Fatal(err)
	}
	abs, err := filepath.Abs("./" + name)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	fileCache := filepath.Join(cache, hex.EncodeToString(digest[:])+image.SquashExt)
	fileAction := Action{
		Type: TypeMount, Image: "./" + name,
		Mountpoint: "/ws/file", Mode: ModeRO, Status: StatusMounted,
	}
	e.CacheEnabled = false
	e.ImagesDir = ""
	if !e.diskMatch("file", fileAction, []DiskImage{{Point: fileAction.Mountpoint, Image: abs}}) {
		t.Fatal("relative file did not match the absolute path attach used")
	}
	if e.diskMatch("file", fileAction, []DiskImage{{Point: fileAction.Mountpoint, Image: fileCache}}) {
		t.Fatal("cache off matched a cache copy of a file image")
	}
	e.CacheEnabled = true
	if !e.diskMatch("file", fileAction, []DiskImage{{Point: fileAction.Mountpoint, Image: fileCache}}) {
		t.Fatal("cache on did not match the cache copy of the file image")
	}
}
