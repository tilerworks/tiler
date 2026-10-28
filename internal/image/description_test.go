package image

import (
	"errors"
	"testing"
)

func TestNormalizeDescriptionPreservesTextAndExtractsSummary(t *testing.T) {
	for _, tc := range []struct {
		name, input, want, summary string
	}{
		{name: "empty"},
		{name: "only newlines", input: "\r\n\n", want: ""},
		{name: "unicode", input: "工具更新\n详细说明", want: "工具更新\n详细说明", summary: "工具更新"},
		{name: "windows newlines", input: "Subject\r\n\r\nBody\r\n", want: "Subject\n\nBody", summary: "Subject"},
		{name: "preserve spaces", input: "  Subject \t\n\n body \n\tlast \n\n", want: "  Subject \t\n\n body \n\tlast ", summary: "  Subject \t"},
		{name: "literal carriage return", input: "Subject\rBody", want: "Subject\rBody", summary: "Subject\rBody"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeDescription(tc.input)
			if err != nil || got != tc.want {
				t.Fatalf("normalize = %q, %v; want %q", got, err, tc.want)
			}
			if summary := DescriptionSummary(got); summary != tc.summary {
				t.Fatalf("summary = %q, want %q", summary, tc.summary)
			}
			if again, err := NormalizeDescription(got); err != nil || again != got {
				t.Fatalf("normalization is not idempotent: %q, %v", again, err)
			}
		})
	}
}

func TestNormalizeDescriptionRejectsMalformedTextAndBlankFirstLines(t *testing.T) {
	for _, input := range []string{"\xff", "Subject\x00Body", "\nBody", " \t\nBody", "\u2003\nBody", " \n"} {
		if _, err := NormalizeDescription(input); !errors.Is(err, ErrInvalidDescription) {
			t.Errorf("normalize %q: expected invalid description, got %v", input, err)
		}
	}
}

func TestDescriptionFromMessagesPreservesParagraphs(t *testing.T) {
	for _, tc := range []struct {
		messages []string
		want     string
	}{
		{messages: nil, want: ""},
		{messages: []string{""}, want: ""},
		{messages: []string{"Subject", "Body\nDetails", "More\r\n"}, want: "Subject\n\nBody\nDetails\n\nMore"},
		{messages: []string{"Subject", "", "Body"}, want: "Subject\n\n\n\nBody"},
	} {
		if got, err := DescriptionFromMessages(tc.messages); err != nil || got != tc.want {
			t.Errorf("messages %q = %q, %v; want %q", tc.messages, got, err, tc.want)
		}
	}
	for _, messages := range [][]string{{"", "Body"}, {"Subject", "\xff"}, {"Subject", "\x00"}} {
		if _, err := DescriptionFromMessages(messages); !errors.Is(err, ErrInvalidDescription) {
			t.Errorf("messages %q: expected invalid description, got %v", messages, err)
		}
	}
}
