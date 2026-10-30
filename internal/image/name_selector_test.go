package image

import (
	"slices"
	"testing"
)

func TestNameSelectorMatchesCompleteNames(t *testing.T) {
	for _, tc := range []struct {
		pattern, name string
		want          bool
	}{
		{"team/app", "team/app", true}, {"app", "team/app", false},
		{"team/*", "team/tools/app", true}, {"team/**", "team/app", true},
		{"team?app", "team/app", true}, {"team/?", "team/界", true},
		{"[a-z]/*", "a/app", true}, {"[^a]/*", "a/app", false},
		{`team/\*`, "team/*", true}, {`team/[\d]`, "team/d", true},
		{`team/[\d]`, "team/5", false}, {`team/[\]]`, "team/]", true},
		{`team/[\-]`, "team/-", true}, {"[.-0]", "/", true},
		{"a.b", "axb", false}, {"", "team/app", false},
		{"*", "team/app", true}, {"[a-c]", "b", true},
	} {
		t.Run(tc.pattern+":"+tc.name, func(t *testing.T) {
			selector, err := NewNameSelector([]string{tc.pattern})
			if err != nil {
				t.Fatal(err)
			}
			got, _ := selector.Select([]string{tc.name})
			if (len(got) == 1) != tc.want {
				t.Fatalf("selected %v, want match %v", got, tc.want)
			}
		})
	}
}

func TestNameSelectorRejectsMalformedPatterns(t *testing.T) {
	for _, pattern := range []string{"[", "[]", "[^]", "[a-]", "[z-a]", "a\\", "ok*["} {
		if _, err := NewNameSelector([]string{"*", pattern}); err == nil {
			t.Errorf("accepted invalid pattern %q", pattern)
		}
	}
}

func TestNameSelectorUnionAndEmptySelection(t *testing.T) {
	selector, err := NewNameSelector([]string{"team/*", "team/app", "missing", "none/*"})
	if err != nil {
		t.Fatal(err)
	}
	names := []string{"other/app", "team/tools/app", "team/app", "team/app"}
	selected, unmatched := selector.Select(names)
	if !slices.Equal(selected, []string{"team/app", "team/tools/app"}) ||
		!slices.Equal(unmatched, []string{"missing", "none/*"}) {
		t.Fatalf("selected=%v unmatched=%v", selected, unmatched)
	}
	empty, _ := selector.Select([]string{"other/app"})
	if empty == nil || len(empty) != 0 {
		t.Fatalf("explicit empty selection=%v", empty)
	}
	all, unmatched := (NameSelector{}).Select(names)
	if !slices.Equal(all, []string{"other/app", "team/app", "team/tools/app"}) || len(unmatched) != 0 {
		t.Fatalf("unrestricted selection=%v unmatched=%v", all, unmatched)
	}
}
