package image

import (
	"errors"
	"testing"
)

func TestParseNameAcceptsOwnerRepo(t *testing.T) {
	owner, repo, err := ParseName("alice/app")
	if err != nil {
		t.Fatal(err)
	}
	if owner != "alice" || repo != "app" {
		t.Fatalf("got %s/%s", owner, repo)
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
		"alice//app",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := ParseName(name)
			if !errors.Is(err, ErrInvalidName) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}
