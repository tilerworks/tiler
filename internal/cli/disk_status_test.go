package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jiangxin/tiler/internal/container"
	"github.com/jiangxin/tiler/internal/mountinfo"
	"github.com/jiangxin/tiler/internal/session"
)

func TestStatusReconcilesHdiutilInfo(t *testing.T) {
	home, _ := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	sessions := sessionsDir(home)
	devPoint := filepath.Join(home, "ws", "dev")
	gonePoint := filepath.Join(home, "ws", "gone")
	orphanPoint := filepath.Join(home, "ws", "orphan")
	devImage := filepath.Join(imagesDir, "alice", "app", "image.dmg")
	devShadow := filepath.Join(sessions, "dev", "runtime", "m1", "upper.shadow")
	orphanImage := filepath.Join(imagesDir, "carol", "tool", "image.dmg")
	orphanShadow := filepath.Join(sessions, "orphan", "runtime", "m1", "upper.shadow")
	doc := hdiutilStatusPlist([]statusVolume{
		{image: devImage, shadow: devShadow, point: devPoint},
		{image: orphanImage, shadow: orphanShadow, point: orphanPoint},
		{image: filepath.Join(home, "foreign.dmg"), point: filepath.Join(home, "Foreign")},
	})
	images := mustDiskImages(t, doc)
	withSessionSeams(t, func(engine *session.Engine) {
		engine.LoadDiskImages = func() ([]session.DiskImage, error) { return images, nil }
	})
	writeSessionRecord(t, sessions, "dev", libraryAction("alice/app", devPoint, session.ModeRW))
	writeSessionRecord(t, sessions, "gone", libraryAction("alice/app", gonePoint, session.ModeRW))

	stdout, stderr, _, err := runCmd(t, append(lib, "status")...)
	if err != nil {
		t.Fatalf("status: %v\n%s", err, stderr)
	}
	if !strings.Contains(stdout, "health=healthy") || !strings.Contains(stdout, "health=stale") {
		t.Fatalf("stdout = %q, want healthy and stale", stdout)
	}
	wantOrphan := "orphan " + orphanPoint + " dmg " + filepath.Dir(orphanShadow)
	if !strings.Contains(stdout, wantOrphan) {
		t.Fatalf("stdout = %q, want %q", stdout, wantOrphan)
	}
	if strings.Contains(stdout, "Foreign") {
		t.Fatalf("stdout = %q, want no foreign volume", stdout)
	}

	stdout, stderr, _, err = runCmd(t, append(lib, "status", filepath.Join(devPoint, "src", "main.go"))...)
	if err != nil {
		t.Fatalf("status path: %v\n%s", err, stderr)
	}
	if !strings.Contains(stdout, "dev active mountpoint="+devPoint) || strings.Contains(stdout, " stale") {
		t.Fatalf("stdout = %q, want the healthy mount and a path inside it", stdout)
	}
}

func TestSessionCloseDetachesInReverseAndRemoveKeepsImage(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	sessions := sessionsDir(home)
	withSessionLockSeam(t)
	image := filepath.Join(imagesDir, "alice", "app", "image.dmg")
	if err := os.MkdirAll(filepath.Dir(image), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(image, []byte("dmg-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	view1 := filepath.Join(work, "view1")
	view2 := filepath.Join(work, "view2")
	shadow1 := filepath.Join(sessions, "dev", "runtime", "m1", "upper.shadow")
	shadow2 := filepath.Join(sessions, "dev", "runtime", "m2", "upper.shadow")
	for _, shadow := range []string{shadow1, shadow2} {
		if err := os.MkdirAll(filepath.Dir(shadow), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(shadow, []byte("keep"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeSessionRecord(t, sessions, "dev",
		libraryAction("alice/app", view1, session.ModeRW),
		libraryAction("alice/app", view2, session.ModeRW))
	restoreDiskSeams(t)
	loadMountTable = func() ([]mountinfo.Mount, error) {
		return nil, mountinfo.ErrUnavailable
	}
	readHdiutilInfo = func() ([]byte, error) {
		return hdiutilStatusPlist([]statusVolume{
			{image: image, shadow: shadow1, point: view1},
			{image: image, shadow: shadow2, point: view2},
		}), nil
	}
	var calls [][]string
	runHdiutil = func(argv []string, _ error) ([]byte, error) {
		calls = append(calls, append([]string(nil), argv...))
		if len(argv) > 1 && argv[1] == "info" {
			return []byte(`<plist version="1.0"><dict></dict></plist>`), nil
		}
		return nil, nil
	}

	stdout, stderr, err := mustClose(t, lib, "dev")
	if err != nil {
		t.Fatalf("session close: %v\n%s", err, stderr)
	}
	if stdout != "closed dev\n" {
		t.Fatalf("close stdout = %q", stdout)
	}
	detach1, err := container.HdiutilDetachArgv(view2)
	if err != nil {
		t.Fatal(err)
	}
	detach2, err := container.HdiutilDetachArgv(view1)
	if err != nil {
		t.Fatal(err)
	}
	info := container.HdiutilInfoArgv()
	want := [][]string{detach1, info, detach2, info}
	if len(calls) != len(want) {
		t.Fatalf("calls = %q, want reverse detach then info for each", calls)
	}
	for i := range want {
		if strings.Join(calls[i], " ") != strings.Join(want[i], " ") {
			t.Fatalf("call %d = %q, want %q", i, calls[i], want[i])
		}
	}
	for _, shadow := range []string{shadow1, shadow2} {
		body, err := os.ReadFile(shadow)
		if err != nil || string(body) != "keep" {
			t.Fatalf("shadow %s = %q, %v", shadow, body, err)
		}
	}
	if body, err := os.ReadFile(image); err != nil || string(body) != "dmg-bytes" {
		t.Fatalf("image = %q, %v", body, err)
	}

	stdout, stderr, _, err = runCmd(t, append(lib, "session", "remove", "dev")...)
	if err != nil {
		t.Fatalf("session remove: %v\n%s", err, stderr)
	}
	if stdout != "removed dev\n" {
		t.Fatalf("remove stdout = %q", stdout)
	}
	if _, err := os.Stat(filepath.Join(sessions, "dev")); !os.IsNotExist(err) {
		t.Fatalf("session runtime survived remove: %v", err)
	}
	if body, err := os.ReadFile(image); err != nil || string(body) != "dmg-bytes" {
		t.Fatalf("image after remove = %q, %v", body, err)
	}
}

func TestSessionCloseRMDeletesRuntimeAndKeepsImage(t *testing.T) {
	home, work := withWorkspace(t)
	imagesDir, lib := libFlags(home)
	sessions := sessionsDir(home)
	withSessionLockSeam(t)
	image := filepath.Join(imagesDir, "alice", "app", "image.dmg")
	if err := os.MkdirAll(filepath.Dir(image), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(image, []byte("dmg-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	view := filepath.Join(work, "view")
	shadow := filepath.Join(sessions, "dev", "runtime", "m1", "upper.shadow")
	if err := os.MkdirAll(filepath.Dir(shadow), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shadow, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeRemovableSessionRecord(t, sessions, "dev",
		libraryAction("alice/app", view, session.ModeRW))
	restoreDiskSeams(t)
	loadMountTable = func() ([]mountinfo.Mount, error) {
		return nil, mountinfo.ErrUnavailable
	}
	readHdiutilInfo = func() ([]byte, error) {
		return hdiutilStatusPlist([]statusVolume{
			{image: image, shadow: shadow, point: view},
		}), nil
	}
	runHdiutil = func(argv []string, _ error) ([]byte, error) {
		if len(argv) > 1 && argv[1] == "info" {
			return []byte(`<plist version="1.0"><dict></dict></plist>`), nil
		}
		return nil, nil
	}

	stdout, stderr, err := mustClose(t, lib, "dev")
	if err != nil {
		t.Fatalf("session close --rm: %v\n%s", err, stderr)
	}
	if stdout != "removed dev\n" {
		t.Fatalf("close stdout = %q", stdout)
	}
	if _, err := os.Stat(shadow); !os.IsNotExist(err) {
		t.Fatalf("--rm left the shadow: %v", err)
	}
	if body, err := os.ReadFile(image); err != nil || string(body) != "dmg-bytes" {
		t.Fatalf("image = %q, %v", body, err)
	}
}

func TestSessionOpenReplaysOriginalShadowWhenTableUnavailable(t *testing.T) {
	home, work := withWorkspace(t)
	_, lib := libFlags(home)
	sessions := sessionsDir(home)
	f := withMountSeams(t, nil)
	bare := filepath.Join(work, "bare.sqsh")
	writeSquashFile(t, bare)
	view := filepath.Join(work, "view")
	writeSessionRecord(t, sessions, "dev", session.Action{
		Type: session.TypeMount, Image: bare,
		Mountpoint: view, Mode: session.ModeRW,
	})
	shadow := filepath.Join(sessions, "dev", "runtime", "m1", "upper.shadow")
	if err := os.MkdirAll(filepath.Dir(shadow), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shadow, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	restoreDiskSeams(t)
	loadMountTable = func() ([]mountinfo.Mount, error) {
		return nil, mountinfo.ErrUnavailable
	}
	withSessionSeams(t, func(engine *session.Engine) {
		engine.Clean = func(string) error { return nil }
		engine.LoadDiskImages = func() ([]session.DiskImage, error) { return nil, nil }
	})

	stdout, stderr, _, err := runCmd(t, append(lib, "session", "open", "dev")...)
	if err != nil {
		t.Fatalf("session open: %v\n%s", err, stderr)
	}
	if stdout != "opened dev\n" {
		t.Fatalf("open stdout = %q", stdout)
	}
	if len(f.mountReqs) != 1 {
		t.Fatalf("engine calls = %d, want one replay", len(f.mountReqs))
	}
	if f.mountReqs[0].RuntimeDir != filepath.Dir(shadow) {
		t.Fatalf("runtime = %s, want %s", f.mountReqs[0].RuntimeDir, filepath.Dir(shadow))
	}
	body, err := os.ReadFile(shadow)
	if err != nil || string(body) != "keep" {
		t.Fatalf("shadow = %q, %v", body, err)
	}
	if n := countUpperShadows(t, sessions); n != 1 {
		t.Fatalf("upper.shadow files = %d, want 1", n)
	}

	withSessionSeams(t, func(engine *session.Engine) {
		engine.LoadDiskImages = func() ([]session.DiskImage, error) {
			return []session.DiskImage{{
				Point: view, Image: bare, Shadow: shadow,
			}}, nil
		}
	})
	stdout, stderr, _, err = runCmd(t, append(lib, "session", "open", "dev")...)
	if err != nil {
		t.Fatalf("second open: %v\n%s", err, stderr)
	}
	if stdout != "opened dev\n" {
		t.Fatalf("second open stdout = %q", stdout)
	}
	if len(f.mountReqs) != 1 {
		t.Fatalf("a live mount was replayed: %d calls", len(f.mountReqs))
	}
	if n := countUpperShadows(t, sessions); n != 1 {
		t.Fatalf("upper.shadow files = %d after the second open, want 1", n)
	}
}

func restoreDiskSeams(t *testing.T) {
	t.Helper()
	origTable, origInfo, origRun := loadMountTable, readHdiutilInfo, runHdiutil
	t.Cleanup(func() {
		loadMountTable, readHdiutilInfo, runHdiutil = origTable, origInfo, origRun
	})
}

type statusVolume struct {
	image  string
	shadow string
	point  string
}

func hdiutilStatusPlist(volumes []statusVolume) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	b.WriteString(`<plist version="1.0"><dict><key>images</key><array>`)
	for _, vol := range volumes {
		b.WriteString(`<dict><key>image-path</key><string>`)
		b.WriteString(vol.image)
		b.WriteString(`</string>`)
		if vol.shadow != "" {
			b.WriteString(`<key>shadow-path</key><string>`)
			b.WriteString(vol.shadow)
			b.WriteString(`</string>`)
		}
		b.WriteString(`<key>system-entities</key><array><dict><key>mount-point</key><string>`)
		b.WriteString(vol.point)
		b.WriteString(`</string></dict></array></dict>`)
	}
	b.WriteString(`</array></dict></plist>`)
	return []byte(b.String())
}

func mustDiskImages(t *testing.T, doc []byte) []session.DiskImage {
	t.Helper()
	parsed, err := container.ParseHdiutilInfo(doc)
	if err != nil {
		t.Fatal(err)
	}
	images := make([]session.DiskImage, len(parsed))
	for i, item := range parsed {
		images[i] = session.DiskImage{Point: item.MountPoint, Image: item.ImagePath, Shadow: item.ShadowPath}
	}
	return images
}

func TestDetachDiskMountResolvesMountPoint(t *testing.T) {
	restoreDiskSeams(t)
	base := t.TempDir()
	concrete := filepath.Join(base, "concrete")
	if err := os.Mkdir(concrete, 0o755); err != nil {
		t.Fatal(err)
	}
	recorded := filepath.Join(base, "mp")
	if err := os.Symlink(concrete, recorded); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(recorded)
	if err != nil {
		t.Fatal(err)
	}
	if resolved == recorded {
		t.Fatal("symlink fixture resolved to the recorded mount point")
	}
	readHdiutilInfo = func() ([]byte, error) {
		return hdiutilStatusPlist([]statusVolume{{
			image:  "/images/alice/app/image.dmg",
			shadow: "/sessions/dev/runtime/m1/upper.shadow",
			point:  resolved,
		}}), nil
	}
	var calls [][]string
	runHdiutil = func(argv []string, _ error) ([]byte, error) {
		calls = append(calls, append([]string(nil), argv...))
		if len(argv) > 1 && argv[1] == "info" {
			return []byte(`<plist version="1.0"><dict></dict></plist>`), nil
		}
		return nil, nil
	}

	if err := detachDiskMount(recorded); err != nil {
		t.Fatal(err)
	}
	detach, err := container.HdiutilDetachArgv(recorded)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{detach, container.HdiutilInfoArgv()}
	if len(calls) != len(want) {
		t.Fatalf("calls = %q, want detach of the recorded mount point", calls)
	}
	for i := range want {
		if strings.Join(calls[i], " ") != strings.Join(want[i], " ") {
			t.Fatalf("call %d = %q, want %q", i, calls[i], want[i])
		}
	}
}

func countUpperShadows(t *testing.T, root string) int {
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
