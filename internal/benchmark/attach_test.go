package benchmark

import (
	"strings"
	"testing"
)

func TestAttachTarDoesNotMount(t *testing.T) {
	rec := &argvRec{}
	_, err := Attach(rec.command, Host{}, "tar", "/bench/out.tar.gz", packPoint)
	if err == nil || !strings.Contains(err.Error(), "not mounted") {
		t.Fatalf("Attach error = %v", err)
	}
	if len(rec.calls) != 0 {
		t.Fatalf("calls = %v", rec.calls)
	}
}

func TestAttachRejectsNilRunner(t *testing.T) {
	if _, err := Attach(nil, Host{}, "dmg", packImage, packPoint); err == nil {
		t.Fatal("expected nil runner error")
	}
	if err := Detach(nil, Mount{Tool: "hdiutil"}, packPoint); err == nil {
		t.Fatal("expected nil runner error")
	}
}
