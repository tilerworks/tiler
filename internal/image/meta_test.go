package image

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestIDUsesSHA256Prefix(t *testing.T) {
	sum := sha256.Sum256([]byte("tiler"))
	got := ID(sum)
	want := hex.EncodeToString(sum[:])[:idHexLen]
	if got != want {
		t.Fatalf("id = %s, want %s", got, want)
	}
}

func TestIDHexLenFloor(t *testing.T) {
	if idHexLen < 16 {
		t.Fatalf("idHexLen = %d, want >= 16", idHexLen)
	}
}

func TestValidateReportsEveryViolation(t *testing.T) {
	const sum = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	meta := Meta{
		ID:     "0123456789ABCDEF",
		Owner:  "..",
		Repo:   "",
		Kind:   "blob",
		Format: Format{Type: "tar", Compression: "gzip"},
		Size:   0,
		SHA256: strings.ToUpper(sum),
		Source: Source{Commit: "ABC"},
		CreatedAt: time.Date(
			2026, 10, 16, 8, 0, 0, 0, time.FixedZone("CST", 8*3600),
		),
		MtimeUnix: -1,
		Chain: []ChainEntry{{
			ID:        "fedcba9876543210",
			Parent:    "unknown",
			Size:      0,
			SHA256:    sum,
			MtimeUnix: -2,
			Source:    Source{Commit: "ZZ"},
		}},
	}
	err := meta.Validate()
	if err == nil {
		t.Fatal("expected violations")
	}
	text := err.Error()
	wants := []string{
		"id: must be 16 lowercase hex digits",
		"sha256: must be 64 lowercase hex digits",
		"owner: invalid segment",
		"repo: invalid segment",
		"kind: must be full or incremental",
		"builder: must be set",
		"source.commit: must be lowercase hex",
		"format.type: unknown container type: \"tar\"",
		"size: must be greater than 0",
		"mtime_unix: must not be negative",
		"created_at: must be UTC",
		"chain[0].id: must be the first 16 hex digits of sha256",
		"chain[0].parent: must continue the linear chain",
		"chain[0].size: must be greater than 0",
		"chain[0].mtime_unix: must not be negative",
		"chain[0].created_at: must be set",
		"chain[0].source.commit: must be lowercase hex",
	}
	for _, want := range wants {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in %s", want, text)
		}
	}
}

func TestValidateRejectsDuplicateAndBrokenChain(t *testing.T) {
	base := validMeta()
	base.Chain = []ChainEntry{
		validEntry(base.ID, "fedcba9876543210"),
		validEntry(base.ID, "0011223344556677"),
	}
	base.Chain[1].ID = base.Chain[0].ID
	base.Chain[1].SHA256 = base.Chain[0].SHA256
	err := base.Validate()
	if err == nil {
		t.Fatal("expected chain violations")
	}
	text := err.Error()
	if !strings.Contains(text, "chain[1].parent: must continue the linear chain") {
		t.Fatalf("missing broken parent: %s", text)
	}
	if !strings.Contains(text, "chain[1].id: duplicate") {
		t.Fatalf("missing duplicate: %s", text)
	}
}

func TestValidateRejectsSlashInSegments(t *testing.T) {
	meta := validMeta()
	meta.Owner = "foo/../../etc"
	meta.Repo = "app/../../tmp"
	err := meta.Validate()
	if err == nil {
		t.Fatal("expected slash segments to fail")
	}
	text := err.Error()
	for _, want := range []string{"owner: invalid segment", "repo: invalid segment"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in %s", want, text)
		}
	}
}

func TestValidateAcceptsZeroOffsetUTC(t *testing.T) {
	meta := validMeta()
	meta.CreatedAt = time.Date(2026, 10, 16, 8, 0, 0, 0, time.FixedZone("UTC", 0))
	if err := meta.Validate(); err != nil {
		t.Fatal(err)
	}

	encoded, err := Encode(validMeta())
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(
		string(encoded),
		"2026-10-16T08:00:00Z",
		"2026-10-16T08:00:00+00:00",
		1,
	)
	if _, err := Decode([]byte(body)); err != nil {
		t.Fatal(err)
	}
}

func TestLoadEmptyDocumentPrefixesOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "meta.yaml")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || err.Error() != "meta: empty document" {
		t.Fatalf("error = %v", err)
	}
}

func TestMetaYAMLRoundTripDropsUnknownFields(t *testing.T) {
	meta := validMeta()
	meta.Version = "1.2.3"
	meta.Source.Commit = "abc123"
	meta.Chain = []ChainEntry{validEntry(meta.ID, "fedcba9876543210")}

	encoded, err := Encode(meta)
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(string(encoded), "type: squashfs\n", "type: squashfs\n    extra: 1\n", 1)
	body += "future: dropped\n"
	got, err := Decode([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if err := got.Validate(); err != nil {
		t.Fatal(err)
	}
	again, err := Encode(got)
	if err != nil {
		t.Fatal(err)
	}
	text := string(again)
	if strings.Contains(text, "future") || strings.Contains(text, "extra") {
		t.Fatalf("unknown field kept: %s", text)
	}
	if got.ID != meta.ID || got.Version != meta.Version || got.Source.Commit != "abc123" {
		t.Fatalf("round trip changed fields: %+v", got)
	}
	if len(got.Chain) != 1 || got.Chain[0].Parent != meta.ID {
		t.Fatalf("chain = %+v", got.Chain)
	}

	path := filepath.Join(t.TempDir(), "meta.yaml")
	if err := Store(path, meta); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SHA256 != meta.SHA256 || loaded.CreatedAt.Location() != time.UTC {
		t.Fatalf("loaded = %+v", loaded)
	}
	if loaded.Deprecated || loaded.Kind != KindFull {
		t.Fatalf("loaded flags = %+v", loaded)
	}
}

func TestSplitManifestRoundTrip(t *testing.T) {
	meta := validBlocksMeta()
	encoded, err := Encode(meta)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Blocks, meta.Blocks) {
		t.Errorf("blocks = %+v, want %+v", got.Blocks, meta.Blocks)
	}
	if got.ID != meta.ID || got.SHA256 != meta.SHA256 ||
		got.Size != meta.Size {
		t.Errorf("identity changed on round trip: %+v", got)
	}
}

func TestSingleFileMetaOmitsSplitKeys(t *testing.T) {
	encoded, err := Encode(validMeta())
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, key := range []string{"blocks:", "members:"} {
		if strings.Contains(text, key) {
			t.Fatalf("single-file meta grew %s:\n%s", key, text)
		}
	}
	got, err := Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Blocks) != 0 || len(got.Members) != 0 {
		t.Fatalf("split manifests appeared: %+v", got)
	}
}

func validMeta() Meta {
	const sum = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	return Meta{
		ID:        sum[:idHexLen],
		Owner:     "alice",
		Repo:      "app",
		Kind:      KindFull,
		Format:    Format{Type: FormatSquashfs, Compression: CompressionZstd},
		Size:      12345,
		SHA256:    sum,
		MtimeUnix: 1710000000,
		CreatedAt: time.Date(2026, 10, 16, 8, 0, 0, 0, time.UTC),
		Builder:   "dev",
	}
}

func validEntry(parent, id string) ChainEntry {
	return ChainEntry{
		ID:        id,
		Parent:    parent,
		Size:      4096,
		SHA256:    id + id + id + id,
		MtimeUnix: 1710000001,
		CreatedAt: time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC),
	}
}

func TestValidateAcceptsRegisteredContainers(t *testing.T) {
	formats := []Format{
		{Type: FormatSquashfs, Compression: CompressionZstd},
		{Type: FormatSquashfs, Compression: "gzip"},
		{Type: FormatSquashfs, Compression: "lz4"},
		{Type: FormatSquashfs, Compression: "xz"},
		{Type: FormatErofs, Compression: CompressionZstd},
		{Type: FormatErofs, Compression: "lz4"},
		{Type: FormatErofs, Compression: "gzip"},
	}
	for _, format := range formats {
		meta := validMeta()
		meta.Format = format
		meta.Chain = []ChainEntry{validEntry(meta.ID, "fedcba9876543210")}
		if err := meta.Validate(); err != nil {
			t.Errorf("%s/%s: %v", format.Type, format.Compression, err)
		}
	}
}

func TestValidateRejectsDisallowedCompression(t *testing.T) {
	meta := validMeta()
	meta.Format = Format{Type: FormatErofs, Compression: "xz"}
	err := meta.Validate()
	if err == nil || !strings.Contains(err.Error(), `format.compression: erofs does not allow "xz"`) {
		t.Fatalf("erofs xz: %v", err)
	}
	meta.Format = Format{Type: FormatSquashfs, Compression: ""}
	err = meta.Validate()
	if err == nil || !strings.Contains(err.Error(), `format.compression: squashfs does not allow ""`) {
		t.Fatalf("empty compression: %v", err)
	}
}
