//go:build darwin

package benchmark

import (
	"errors"
	"strings"
	"testing"

	"github.com/jiangxin/tiler/internal/container"
)

func TestAttachDMGMatchesReadOnlyArgv(t *testing.T) {
	rec := &argvRec{}
	got, err := Attach(rec.command, Host{}, "dmg", packImage, packPoint)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if got.Tool != "hdiutil" {
		t.Fatalf("tool = %q", got.Tool)
	}
	want, err := container.HdiutilAttachReadOnlyArgv(packImage, packPoint)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.calls) != 1 {
		t.Fatalf("calls = %v", rec.calls)
	}
	assertHdiutil(t, rec.calls[0], want)
}

func TestDetachDMGStopsAfterHdiutil(t *testing.T) {
	rec := &argvRec{}
	if err := Detach(rec.command, Mount{Tool: "hdiutil"}, packPoint); err != nil {
		t.Fatalf("Detach: %v", err)
	}
	want, err := container.HdiutilDetachArgv(packPoint)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.calls) != 1 {
		t.Fatalf("calls = %v", rec.calls)
	}
	assertHdiutil(t, rec.calls[0], want)
}

func TestDetachDMGFallsBackToDiskutilEject(t *testing.T) {
	rec := &argvRec{fail: func(name string, args []string) error {
		if name == "hdiutil" && len(args) > 0 && args[0] == "detach" {
			return errors.New("busy")
		}
		return nil
	}}
	if err := Detach(rec.command, Mount{Tool: "hdiutil"}, packPoint); err != nil {
		t.Fatalf("Detach: %v", err)
	}
	if len(rec.calls) != 2 {
		t.Fatalf("calls = %v", rec.calls)
	}
	want, err := container.HdiutilDetachArgv(packPoint)
	if err != nil {
		t.Fatal(err)
	}
	assertHdiutil(t, rec.calls[0], want)
	eject := rec.calls[1]
	if eject.name != "diskutil" || eject.env != nil || len(eject.args) != 2 ||
		eject.args[0] != "eject" || eject.args[1] != packPoint {
		t.Fatalf("eject = %s", eject)
	}
}

func TestDetachDMGReportsBothFailures(t *testing.T) {
	rec := &argvRec{fail: func(string, []string) error {
		return errors.New("busy")
	}}
	err := Detach(rec.command, Mount{Tool: "hdiutil"}, packPoint)
	if err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("Detach error = %v", err)
	}
	if len(rec.calls) != 2 || rec.calls[1].name != "diskutil" {
		t.Fatalf("calls = %v", rec.calls)
	}
}

func TestAttachSquashfsOnDarwinReturnsError(t *testing.T) {
	rec := &argvRec{}
	_, err := Attach(rec.command, Host{}, "squashfs", "/bench/app.sqsh", packPoint)
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("Attach error = %v", err)
	}
	if len(rec.calls) != 0 {
		t.Fatalf("calls = %v", rec.calls)
	}
}
