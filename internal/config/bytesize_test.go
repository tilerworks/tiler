package config

import "testing"

func TestParseByteSize(t *testing.T) {
	const twoGiB = int64(2) << 30
	same := []string{"2G", "2g", "2GB", "2gb", "2Gb"}
	for _, raw := range same {
		got, err := ParseByteSize(raw)
		if err != nil {
			t.Fatalf("ParseByteSize(%q): %v", raw, err)
		}
		if got != twoGiB {
			t.Fatalf("ParseByteSize(%q) = %d, want %d", raw, got, twoGiB)
		}
	}

	cases := []struct {
		raw  string
		want int64
	}{
		{raw: "0", want: 0},
		{raw: "1024", want: 1024},
		{raw: " 2048 ", want: 2048},
		{raw: "2k", want: 2 << 10},
		{raw: "2K", want: 2 << 10},
		{raw: "2kb", want: 2 << 10},
		{raw: "2KB", want: 2 << 10},
		{raw: "3m", want: 3 << 20},
		{raw: "3MB", want: 3 << 20},
		{raw: "4t", want: 4 << 40},
		{raw: "4TB", want: 4 << 40},
		{raw: "0G", want: 0},
	}
	for _, tc := range cases {
		got, err := ParseByteSize(tc.raw)
		if err != nil {
			t.Fatalf("ParseByteSize(%q): %v", tc.raw, err)
		}
		if got != tc.want {
			t.Fatalf("ParseByteSize(%q) = %d, want %d", tc.raw, got, tc.want)
		}
	}
}

func TestParseByteSizeRejects(t *testing.T) {
	for _, raw := range []string{
		"",
		" ",
		"-1",
		"-2G",
		"1.5",
		"1.5G",
		"2.0",
		"2ki",
		"2Ki",
		"2Mi",
		"2GiB",
		"2p",
		"2b",
		"G",
		"kb",
		"2e3",
		"2 G",
		"+2",
		"8388608T",
	} {
		if _, err := ParseByteSize(raw); err == nil {
			t.Errorf("ParseByteSize(%q) succeeded", raw)
		}
	}
	_, err := ParseByteSize("-2")
	if err == nil || err.Error() != "must not be negative" {
		t.Fatalf("negative error = %v", err)
	}
}
