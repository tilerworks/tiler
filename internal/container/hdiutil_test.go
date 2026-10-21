package container

import (
	"errors"
	"path/filepath"
	"slices"
	"testing"
)

func TestHdiutilArgv(t *testing.T) {
	shadow, err := DMGShadowPath("sessions", "dev", 3)
	if err != nil {
		t.Fatal(err)
	}
	wantShadow := filepath.Join("sessions", "dev", "runtime", "m3", "upper.shadow")
	if shadow != wantShadow {
		t.Fatalf("shadow = %q", shadow)
	}

	src := "/src/app"
	image := "images/alice/app/image.dmg"
	temp := "images/alice/app/image.dmg.tmp"
	point := "/work/app"

	cases := []struct {
		name string
		call func() ([]string, error)
		want []string
	}{
		{
			name: "create default ulfo",
			call: func() ([]string, error) {
				return HdiutilCreateArgv(src, "", "alice/app", temp)
			},
			want: []string{
				"hdiutil", "create", "-srcfolder", src,
				"-format", "ULFO", "-fs", "APFS", "-volname", "app", temp,
			},
		},
		{
			name: "create ulmo",
			call: func() ([]string, error) {
				return HdiutilCreateArgv(src, "ulmo", "app", temp)
			},
			want: []string{
				"hdiutil", "create", "-srcfolder", src,
				"-format", "ULMO", "-fs", "APFS", "-volname", "app", temp,
			},
		},
		{
			name: "create udro",
			call: func() ([]string, error) {
				return HdiutilCreateArgv(src, "udro", "alice/app", temp)
			},
			want: []string{
				"hdiutil", "create", "-srcfolder", src,
				"-format", "UDRO", "-fs", "APFS", "-volname", "app", temp,
			},
		},
		{
			name: "convert keeps base compression",
			call: func() ([]string, error) {
				return HdiutilConvertArgv(image, "", "ulmo", shadow, temp)
			},
			want: []string{
				"hdiutil", "convert", image,
				"-format", "ULMO", "-shadow", shadow, "-o", temp,
			},
		},
		{
			name: "convert uses requested compression",
			call: func() ([]string, error) {
				return HdiutilConvertArgv(image, "udro", "ulfo", shadow, temp)
			},
			want: []string{
				"hdiutil", "convert", image,
				"-format", "UDRO", "-shadow", shadow, "-o", temp,
			},
		},
		{
			name: "writable attach",
			call: func() ([]string, error) {
				return HdiutilAttachWritableArgv(image, point, shadow)
			},
			want: []string{
				"hdiutil", "attach", "-nobrowse", "-noautoopen",
				"-mountpoint", point, "-shadow", shadow, image,
			},
		},
		{
			name: "read-only attach",
			call: func() ([]string, error) {
				return HdiutilAttachReadOnlyArgv(image, point)
			},
			want: []string{
				"hdiutil", "attach", "-nobrowse", "-noautoopen",
				"-mountpoint", point, "-readonly", image,
			},
		},
		{
			name: "detach",
			call: func() ([]string, error) {
				return HdiutilDetachArgv(point)
			},
			want: []string{"hdiutil", "detach", point},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			argv, err := tc.call()
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(argv, tc.want) {
				t.Fatalf("argv = %q\nwant %q", argv, tc.want)
			}
			if slices.Contains(argv, "-force") {
				t.Fatal("argv includes -force")
			}
			runner := &argvRunner{}
			if err := RunHdiutil(runner.run, argv, err); err != nil {
				t.Fatal(err)
			}
			if len(runner.calls) != 1 {
				t.Fatalf("calls = %d", len(runner.calls))
			}
			call := runner.calls[0]
			if call.name != "hdiutil" || !slices.Equal(call.args, argv[1:]) {
				t.Fatalf("call = %s %q", call.name, call.args)
			}
		})
	}

	if got := HdiutilInfoArgv(); !slices.Equal(got, []string{"hdiutil", "info", "-plist"}) {
		t.Fatalf("info = %q", got)
	}
}

func TestHdiutilConvertLeavesShadowInPlace(t *testing.T) {
	shadow, err := DMGShadowPath("/home/u/.tiler/sessions", "dev", 12)
	if err != nil {
		t.Fatal(err)
	}
	argv, err := HdiutilConvertArgv("image.dmg", "ulfo", "udro", shadow, "out.dmg")
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(argv, "rm") || slices.Contains(argv, "mv") {
		t.Fatalf("convert moves the shadow: %q", argv)
	}
	seen := 0
	for _, arg := range argv {
		if arg == shadow {
			seen++
		}
	}
	if seen != 1 || !slices.Contains(argv, "-shadow") {
		t.Fatalf("shadow count = %d argv = %q", seen, argv)
	}
	runner := &argvRunner{}
	if err := RunHdiutil(runner.run, argv, err); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("calls = %d", len(runner.calls))
	}
}

func TestHdiutilReadOnlyOmitsShadow(t *testing.T) {
	shadow, err := DMGShadowPath("sessions", "dev", 1)
	if err != nil {
		t.Fatal(err)
	}
	argv, err := HdiutilAttachReadOnlyArgv("image.dmg", "/mnt")
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(argv, "-shadow") || slices.Contains(argv, shadow) ||
		!slices.Contains(argv, "-readonly") {
		t.Fatalf("argv = %q", argv)
	}
}

func TestHdiutilFailedConstructorIsNotExecuted(t *testing.T) {
	shadow, err := DMGShadowPath("sessions", "dev", 1)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		call func() ([]string, error)
	}{
		{
			name: "create rejects unknown compression",
			call: func() ([]string, error) {
				return HdiutilCreateArgv("/src", "zstd", "alice/app", "t.dmg")
			},
		},
		{
			name: "create rejects a colon in the volume name",
			call: func() ([]string, error) {
				return HdiutilCreateArgv("/src", "ulfo", "alice/my:disk", "t.dmg")
			},
		},
		{
			name: "convert without a base compression",
			call: func() ([]string, error) {
				return HdiutilConvertArgv("image.dmg", "", "", shadow, "t.dmg")
			},
		},
		{
			name: "convert rejects a shadow outside the runtime",
			call: func() ([]string, error) {
				return HdiutilConvertArgv("image.dmg", "ulfo", "ulfo", "/tmp/upper.shadow", "t.dmg")
			},
		},
		{
			name: "writable attach rejects an empty image",
			call: func() ([]string, error) {
				return HdiutilAttachWritableArgv(" ", "/mnt", shadow)
			},
		},
		{
			name: "detach rejects an empty mountpoint",
			call: func() ([]string, error) {
				return HdiutilDetachArgv("")
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner := &argvRunner{}
			argv, consErr := tc.call()
			if consErr == nil {
				t.Fatal("constructor succeeded")
			}
			if argv != nil {
				t.Fatalf("argv = %q", argv)
			}
			if err := RunHdiutil(runner.run, argv, consErr); err == nil {
				t.Fatal("run succeeded")
			}
			if len(runner.calls) != 0 {
				t.Fatalf("executed %d calls", len(runner.calls))
			}
			// A runnable vector is still not executed when the
			// constructor failed.
			ready := []string{"hdiutil", "detach", "/mnt"}
			if err := RunHdiutil(runner.run, ready, consErr); err == nil || len(runner.calls) != 0 {
				t.Fatalf("executed failed constructor err=%v calls=%d", err, len(runner.calls))
			}
		})
	}
}

func TestDMGShadowPathRejectsBadIdentity(t *testing.T) {
	if _, err := DMGShadowPath("", "dev", 1); err == nil {
		t.Fatal("empty sessions dir")
	}
	if _, err := DMGShadowPath("sessions", "..", 1); err == nil {
		t.Fatal("session name ..")
	}
	if _, err := DMGShadowPath("sessions", "dev", 0); err == nil {
		t.Fatal("sequence 0")
	}
}

func TestHdiutilCreateUsesHFSWhenShadowCannotAttachAPFS(t *testing.T) {
	restore := SetDMGCreateFilesystem(func() string { return "HFS+" })
	t.Cleanup(restore)
	argv, err := HdiutilCreateArgv("/src", "ulfo", "app", "t.dmg")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(argv, "HFS+") || slices.Contains(argv, "APFS") {
		t.Fatalf("argv = %q, want HFS+", argv)
	}
	restore()
	argv, err = HdiutilCreateArgv("/src", "ulfo", "app", "t.dmg")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(argv, "APFS") || slices.Contains(argv, "HFS+") {
		t.Fatalf("argv = %q, want APFS again", argv)
	}
}

func TestRunHdiutilRejectsNilRunner(t *testing.T) {
	argv, err := HdiutilDetachArgv("/mnt")
	if err != nil {
		t.Fatal(err)
	}
	if err := RunHdiutil(nil, argv, nil); err == nil {
		t.Fatal("nil runner")
	}
	if err := RunHdiutil(nil, nil, errors.New("constructor")); err == nil {
		t.Fatal("constructor error was dropped")
	}
}
