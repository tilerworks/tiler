package image

import (
	"strings"
	"testing"
	"time"
)

func TestFormatSavedTimeUsesLocalOffset(t *testing.T) {
	when := time.Date(2026, 10, 29, 10, 7, 56, 0, time.UTC)
	got := formatSavedTime(when)
	want := when.In(time.Local).Format(time.RFC3339)
	if got != want {
		t.Fatalf("formatSavedTime = %s, want %s", got, want)
	}
	if strings.Contains(got, ".") {
		t.Fatalf("whole-second timestamp kept a fraction: %s", got)
	}
}

func TestSameSecondBuildsStayOrdered(t *testing.T) {
	base := time.Date(2026, 10, 29, 10, 7, 56, 0, time.UTC)
	manifest := versionFixture()
	manifest.Images[0].CreatedAt = base.Add(100 * time.Millisecond)
	manifest.Images[1].CreatedAt = base.Add(900 * time.Millisecond)
	body, err := EncodeVersionManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeVersionManifest(body)
	if err != nil {
		t.Fatal(err)
	}
	earlier, later := got.Images[0].CreatedAt, got.Images[1].CreatedAt
	if !earlier.Equal(base) || !later.Equal(base) {
		t.Fatalf("saved times = %s then %s, want both %s", earlier, later, base)
	}
	for _, line := range strings.Split(string(body), "\n") {
		if strings.Contains(line, "created_at:") && strings.Contains(line, ".") {
			t.Fatalf("saved created_at kept a fractional second: %s", line)
		}
	}
}

func TestSavedCreatedAtUsesLocalRFC3339(t *testing.T) {
	when := time.Date(2026, 10, 29, 10, 7, 56, 733866959, time.UTC)
	want := formatSavedTime(when)
	manifest := versionFixture()
	manifest.Images[0].CreatedAt = when

	body, err := EncodeVersionManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "created_at: "+want) {
		t.Fatalf("saved created_at is not %s\n%s", want, body)
	}
	got, err := DecodeVersionManifest(body)
	if err != nil {
		t.Fatal(err)
	}
	if loc := got.Images[0].CreatedAt.Location(); loc != time.UTC {
		t.Fatalf("loaded location = %v, want UTC", loc)
	}
	if !got.Images[0].CreatedAt.Equal(when.Truncate(time.Second)) {
		t.Fatalf("loaded = %v, want %v", got.Images[0].CreatedAt, when.Truncate(time.Second))
	}

	legacy := strings.Replace(string(body), "created_at: "+want,
		"created_at: "+when.Format(time.RFC3339Nano), 1)
	got, err = DecodeVersionManifest([]byte(legacy))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Images[0].CreatedAt.Equal(when.Truncate(time.Second)) {
		t.Fatalf("legacy created_at = %v, want %v", got.Images[0].CreatedAt, when.Truncate(time.Second))
	}
}

func TestEncodeManifestSavesLocalCreatedAt(t *testing.T) {
	when := time.Date(2026, 10, 29, 10, 7, 56, 733866959, time.UTC)
	manifest := validManifest()
	manifest.Images[0].CreatedAt = when
	body, err := EncodeManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	want := formatSavedTime(when)
	if !strings.Contains(string(body), "created_at: "+want) {
		t.Fatalf("saved created_at is not %s\n%s", want, body)
	}
	got, err := DecodeManifest(body)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Images[0].CreatedAt.Equal(when.Truncate(time.Second)) ||
		got.Images[0].CreatedAt.Location() != time.UTC {
		t.Fatalf("loaded = %v", got.Images[0].CreatedAt)
	}
}
