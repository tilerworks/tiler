package container

import (
	"errors"
	"testing"
)

func TestInferTypePrefersSquashfsWhenBothStacksExist(t *testing.T) {
	got, err := InferType(lookPresent())
	if err != nil {
		t.Fatal(err)
	}
	if got != squashfsType {
		t.Fatalf("InferType = %q, want squashfs", got)
	}
}

func TestInferTypeSelectsTheOnlyCompleteStack(t *testing.T) {
	tests := []struct {
		name    string
		present map[string]bool
		want    string
	}{
		{
			name:    "erofs only",
			present: toolSet("mkfs.erofs", "erofsfuse"),
			want:    erofsType,
		},
		{
			name:    "squashfs only",
			present: toolSet("mksquashfs", "squashfuse_ll"),
			want:    squashfsType,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := InferType(lookSet(tt.present))
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("InferType = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDmgIsNotASelectableImageStack(t *testing.T) {
	_, err := InferType(lookSet(toolSet("hdiutil")))
	if !errors.Is(err, ErrNoToolchain) {
		t.Fatalf("InferType err = %v, want ErrNoToolchain", err)
	}
	if AnyComplete(lookSet(toolSet("hdiutil"))) {
		t.Fatal("hdiutil alone was a complete image stack")
	}
	got, err := InferType(lookSet(toolSet("hdiutil", "mkfs.erofs", "erofsfuse")))
	if err != nil {
		t.Fatal(err)
	}
	if got != erofsType {
		t.Fatalf("InferType = %q, want erofs", got)
	}
	for _, stack := range Stacks(lookPresent()) {
		if stack.Format.Type() == dmgType {
			t.Fatal("dmg was reported as a selectable stack")
		}
	}
}

func TestInferTypeFailsWhenNoStackIsComplete(t *testing.T) {
	_, err := InferType(lookSet(nil))
	if !errors.Is(err, ErrNoToolchain) {
		t.Fatalf("InferType err = %v, want ErrNoToolchain", err)
	}
	if AnyComplete(lookSet(toolSet("mksquashfs", "mkfs.erofs"))) {
		t.Fatal("a stack with one tool missing was complete")
	}
}

func TestMissingToolsFollowsFormatOrder(t *testing.T) {
	format, err := Lookup(erofsType)
	if err != nil {
		t.Fatal(err)
	}
	got := MissingTools(format, lookSet(toolSet("erofsfuse")))
	want := []string{"mkfs.erofs"}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("MissingTools = %v, want %v", got, want)
	}
}

func lookPresent() func(string) (string, error) {
	return func(name string) (string, error) {
		return "/usr/bin/" + name, nil
	}
}

func lookSet(present map[string]bool) func(string) (string, error) {
	return func(name string) (string, error) {
		if present[name] {
			return "/usr/bin/" + name, nil
		}
		return "", errors.New("not found")
	}
}

func toolSet(names ...string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, name := range names {
		set[name] = true
	}
	return set
}
