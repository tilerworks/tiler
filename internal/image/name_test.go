package image

import (
	"errors"
	"strconv"
	"strings"
	"testing"
)

func TestParseNameAcceptsLegalNames(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"app", []string{"app"}},
		{"alice/app", []string{"alice", "app"}},
		{"a/b/c", []string{"a", "b", "c"}},
		{"a/b/c/d/e", []string{"a", "b", "c", "d", "e"}},
		{"alice/incr", []string{"alice", "incr"}},
		{"alice/abc123", []string{"alice", "abc123"}},
		{"alice/0123456789abcde", []string{"alice", "0123456789abcde"}}, // 15 hex
	}
	for _, tt := range cases {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseName(tt.in)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(got, "/") != strings.Join(tt.want, "/") {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseNameRejectsIllegalShapes(t *testing.T) {
	cases := []string{
		"a/b/c/d/e/f",
		"",
		"/",
		"/app",
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
		"alice/_incr",
		"alice/_archives",
		"alice/.hidden",
		"_alice/app",
		".alice/app",
		"_foo",
		".bar",
		"alice/meta.yaml",
		"alice/image.sqsh",
		"alice/image.dmg",
		"alice/image.sqsh-concat-view",
		"meta.yaml",
		"meta.yaml/app",
		"alice/0123456789abcdef",
		"alice/0123456789ABCDEF",
		"alice/0123456789abcdef0",
		"0123456789abcdef/app",
		"alice/../../bob/app",
		"alice/app:abcd",
		"ali:ce/app",
		"alice/:/app",
		"alice/bad:name/../app",
		"bad:name/../alice/app",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseName(name)
			if !errors.Is(err, ErrInvalidName) {
				t.Fatalf("error = %v", err)
			}
			msg := err.Error()
			if !strings.Contains(msg, "want 1 to 5 segments") {
				t.Fatalf("error = %q, want format guidance", msg)
			}
			if !strings.Contains(msg, strconv.Quote(name)) {
				t.Fatalf("error = %q, want quoted input %s", msg, strconv.Quote(name))
			}
		})
	}
}

func TestParseNameCleansBeforeCheckingShape(t *testing.T) {
	cases := map[string]string{
		"alice//app":       "alice/app",
		"alice/./app":      "alice/app",
		"alice/app/":       "alice/app",
		"alice/foo/../app": "alice/app",
		"./alice/app":      "alice/app",
		"alice/././app":    "alice/app",
		"alice/.":          "alice",
		"./app":            "app",
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := ParseName(name)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(got, "/") != want {
				t.Fatalf("got %v, want %s", got, want)
			}
		})
	}
}

func TestParseNameQuotesRawInputOnFailure(t *testing.T) {
	_, err := ParseName("a/b/c/d/e/f")
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, `"a/b/c/d/e/f"`) {
		t.Fatalf("error = %q, want quoted raw input", msg)
	}
	if !strings.Contains(msg, "want 1 to 5 segments") {
		t.Fatalf("error = %q, want format guidance", msg)
	}
}
