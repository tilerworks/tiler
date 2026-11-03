package diagnostic

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestDeniedMountPreservesCauseAndFormatsToolOutput(t *testing.T) {
	for _, cause := range []error{
		fmt.Errorf("mount: %w", os.ErrPermission),
		errors.New("tool: exit status 1: fusermount3: mount failed: Operation not permitted\nextra\x1b[31m"),
		errors.New("tool: permission denied"),
	} {
		err := DeniedMount("mount failed", cause)
		if err == nil || !errors.Is(err, cause) {
			t.Fatalf("lost cause: %v", err)
		}
		text := err.Error()
		if !strings.Contains(text, "CAP_SYS_ADMIN") || strings.Contains(text, "\x1b") || strings.Contains(text, "\nextra") {
			t.Fatalf("unsafe or incomplete diagnostic: %q", text)
		}
	}
	if err := DeniedMount("mount failed", errors.New("invalid superblock")); err != nil {
		t.Fatalf("unrelated failure gained permission advice: %v", err)
	}
}

func TestTextEscapesDynamicControls(t *testing.T) {
	if got := Text("path\n\x1b"); got != `path\u000a\u001b` {
		t.Fatalf("escaped text = %q", got)
	}
}

func TestPermissionFailureDoesNotAssumeMountWasDenied(t *testing.T) {
	for _, cause := range []error{&os.PathError{Op: "open", Path: "image", Err: os.ErrPermission}, errors.New("squashfuse_ll: cannot open image: Permission denied")} {
		err := DeniedMount("FUSE mount was denied.", cause)
		if strings.Contains(err.Error(), "was denied") || !strings.Contains(err.Error(), "source image") || !errors.Is(err, cause) {
			t.Fatalf("misleading diagnostic: %v", err)
		}
	}
	err := DeniedMount("FUSE mount was denied.", errors.New("fusermount3: mount failed: Operation not permitted"))
	if !strings.Contains(err.Error(), "was denied") {
		t.Fatalf("lost confirmed denial: %v", err)
	}
}

func TestErrorTextPreservesTrustedLayoutAndEscapesWrapper(t *testing.T) {
	cause := errors.New("fusermount3: mount failed: Permission denied\nunsafe\x1b")
	trusted := DeniedMount("mount was denied.", cause)
	wrapped := fmt.Errorf("bad\nwrapper: %w", trusted)
	text := ErrorText(wrapped)
	if !strings.Contains(text, `bad\u000awrapper`) || !strings.Contains(text, "\n  Hint:") || strings.Contains(text, "\x1b") {
		t.Fatalf("unsafe or flattened layout: %q", text)
	}
	if text := ErrorText(errors.New("bad\nerror")); text != `bad\u000aerror` {
		t.Fatalf("untrusted layout preserved: %q", text)
	}
}
