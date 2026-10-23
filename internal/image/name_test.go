package image

import (
	"errors"
	"strconv"
	"strings"
	"testing"
)

func TestParseNameAcceptsOwnerRepo(t *testing.T) {
	cases := []string{
		"alice/app",
		"alice/incr",
		"alice/abc123",
		"alice/0123456789abcde", // 15 hex digits: shorter than idHexLen
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			owner, repo, err := ParseName(name)
			if err != nil {
				t.Fatal(err)
			}
			wantOwner, wantRepo, ok := strings.Cut(cleanImageName(name), "/")
			if !ok || owner != wantOwner || repo != wantRepo {
				t.Fatalf("got %s/%s, want %s/%s", owner, repo, wantOwner, wantRepo)
			}
		})
	}
}

func TestParseNameRejectsIllegalShapes(t *testing.T) {
	cases := []string{
		"alice",
		"alice/app/extra",
		"alice/",
		"/app",
		"",
		"/",
		"alice/.",
		"./app",
		"alice/..",
		"../app",
		"ali ce/app",
		"alice/ap p",
		"alice/app\n",
		"al\x00ice/app",
		"alice/app\x7f",
		"alice/app ",
		" alice/app",
		"alice/ap\u00a0p",
		"alice/ap\u0080p",
		"alice/ap\u009fp",
		"https://example.com/alice/app",
		"alice/_incr",
		"alice/_archives",
		"alice/.hidden",
		"_alice/app",
		".alice/app",
		"alice/meta.yaml",
		"alice/image.sqsh",
		"alice/image.dmg",
		"alice/image.sqsh-concat-view",
		"meta.yaml/app",
		"alice/0123456789abcdef",
		"alice/0123456789ABCDEF",
		"alice/0123456789abcdef0",
		"0123456789abcdef/app",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := ParseName(name)
			if !errors.Is(err, ErrInvalidName) {
				t.Fatalf("error = %v", err)
			}
			msg := err.Error()
			if !strings.Contains(msg, "want owner/repo") {
				t.Fatalf("error = %q, want format guidance", msg)
			}
			if !strings.Contains(msg, strconv.Quote(name)) {
				t.Fatalf("error = %q, want quoted input %s", msg, strconv.Quote(name))
			}
		})
	}
}

func TestParseNameCleansBeforeCheckingShape(t *testing.T) {
	cases := []string{
		"alice//app",
		"alice/./app",
		"alice/app/",
		"alice/foo/../app",
		"./alice/app",
		"alice/././app",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			owner, repo, err := ParseName(name)
			if err != nil {
				t.Fatal(err)
			}
			if owner != "alice" || repo != "app" {
				t.Fatalf("got %s/%s", owner, repo)
			}
		})
	}
}

func TestParseNameRejectsEmptySegmentWithFullInput(t *testing.T) {
	_, _, err := ParseName("alice/")
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, `"alice/"`) {
		t.Fatalf("error = %q, want the full input, not an empty segment", msg)
	}
	if !strings.Contains(msg, "want owner/repo") {
		t.Fatalf("error = %q, want format guidance", msg)
	}
}
