package image

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestIDEqualsPayloadSHA256(t *testing.T) {
	sum := sha256.Sum256([]byte("tiler"))
	got := ID(sum)
	want := hex.EncodeToString(sum[:])
	if got != want {
		t.Fatalf("id = %s, want %s", got, want)
	}
	if len(got) != sumHexLen {
		t.Fatalf("len(id) = %d, want %d", len(got), sumHexLen)
	}
}

func TestIDHexLenFloor(t *testing.T) {
	if idHexLen < 16 {
		t.Fatalf("idHexLen = %d, want >= 16", idHexLen)
	}
}

func TestValidateReportsEveryViolation(t *testing.T) {
	const sum = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	meta := metaV1{
		ID:      "0123456789ABCDEF",
		Name:    "..",
		Version: "2.0",
		Kind:    "blob",
		Format:  Format{Type: "tar", Compression: "gzip"},
		Size:    0,
		SHA256:  strings.ToUpper(sum),
		CreatedAt: time.Date(
			2026, 10, 16, 8, 0, 0, 0, time.FixedZone("CST", 8*3600),
		),
		MtimeUnix: -1,
		Chain: []chainEntryV1{{
			ID:        "fedcba9876543210",
			Parent:    "unknown",
			Size:      0,
			SHA256:    sum,
			MtimeUnix: -2,
		}},
	}
	err := meta.validate()
	if err == nil {
		t.Fatal("expected violations")
	}
	text := err.Error()
	wants := []string{
		"id: must be 64 lowercase hex digits",
		"sha256: must be 64 lowercase hex digits",
		"name: invalid image name",
		`version: unsupported metadata version "2.0"`,
		"kind: must be full or incremental",
		"builder: must be set",
		"format.type: unknown container type: \"tar\"",
		"size: must be greater than 0",
		"mtime_unix: must not be negative",
		"created_at: must be UTC",
		"chain[0].id: must be 64 lowercase hex digits",
		"chain[0].id: must equal sha256",
		"chain[0].parent: must continue the linear chain",
		"chain[0].size: must be greater than 0",
		"chain[0].mtime_unix: must not be negative",
		"chain[0].created_at: must be set",
	}
	for _, want := range wants {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in %s", want, text)
		}
	}
}

func TestValidateRejectsUnknownVersion(t *testing.T) {
	for _, version := range []string{"", "0.9", "2.0", "1"} {
		meta := validMeta()
		meta.Version = version
		err := meta.validate()
		if err == nil {
			t.Fatalf("version %q accepted", version)
		}
		if want := "version: unsupported metadata version"; !strings.Contains(err.Error(), want) {
			t.Fatalf("version %q: error = %v", version, err)
		}
	}
	meta := validMeta()
	meta.Version = MetaVersion
	if err := meta.validate(); err != nil {
		t.Fatalf("MetaVersion rejected: %v", err)
	}
}

func TestValidateRejectsDuplicateAndBrokenChain(t *testing.T) {
	base := validMeta()
	base.Chain = []chainEntryV1{
		validEntry(base.ID, "fedcba9876543210"),
		validEntry(base.ID, "0011223344556677"),
	}
	base.Chain[1].ID = base.Chain[0].ID
	base.Chain[1].SHA256 = base.Chain[0].SHA256
	err := base.validate()
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

func TestValidateRejectsNonCanonicalName(t *testing.T) {
	meta := validMeta()
	meta.Name = "alice//app"
	err := meta.validate()
	if err == nil {
		t.Fatal("expected non-canonical name to fail")
	}
	if !strings.Contains(err.Error(), "name: invalid image name") {
		t.Fatalf("error = %v", err)
	}
}

func TestDecodeManifestRejectsOwnerRepoOnlyIdentity(t *testing.T) {
	encoded, err := yaml.Marshal(validMeta())
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, line := range strings.Split(string(encoded), "\n") {
		if strings.HasPrefix(line, "name:") {
			continue
		}
		lines = append(lines, line)
	}
	body := strings.Join(lines, "\n") + "owner: alice\nrepo: app\n"
	_, err = DecodeManifest([]byte(body))
	if err == nil {
		t.Fatal("expected owner/repo-only document to fail")
	}
	if !strings.Contains(err.Error(), "name: invalid image name") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateAcceptsZeroOffsetUTC(t *testing.T) {
	meta := validMeta()
	meta.CreatedAt = time.Date(2026, 10, 16, 8, 0, 0, 0, time.FixedZone("UTC", 0))
	if err := meta.validate(); err != nil {
		t.Fatal(err)
	}

	encoded, err := yaml.Marshal(validMeta())
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(
		string(encoded),
		"2026-10-16T08:00:00Z",
		"2026-10-16T08:00:00+00:00",
		1,
	)
	if _, err := DecodeManifest([]byte(body)); err != nil {
		t.Fatal(err)
	}
}

func TestLoadManifestPrefixesDecodeErrorsOnce(t *testing.T) {
	manifest := validManifest()
	manifest.Head = ""
	// EncodeManifest validates, so write the broken document the way
	// a corrupted meta.yaml would appear.
	body, err := yaml.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "meta.yaml")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = LoadManifest(path)
	if err == nil || err.Error() != "meta: head: must be set" {
		t.Fatalf("error = %v", err)
	}
}

func TestDecodeManifestDropsUnknownV1Fields(t *testing.T) {
	meta := validMeta()
	meta.Chain = []chainEntryV1{validEntry(meta.ID, "fedcba9876543210")}

	encoded, err := yaml.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(string(encoded), "type: squashfs\n", "type: squashfs\n    extra: 1\n", 1)
	body += "future: dropped\n"
	got, err := DecodeManifest([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Images) != 2 || got.Head != testID("fedcba9876543210") {
		t.Fatalf("converted manifest = %+v", got)
	}

	// The v2 re-encode keeps no unknown field of the v1 document.
	again, err := EncodeManifest(got)
	if err != nil {
		t.Fatal(err)
	}
	text := string(again)
	if strings.Contains(text, "future") || strings.Contains(text, "extra") {
		t.Fatalf("unknown field kept: %s", text)
	}
	if got.Version != ManifestVersion {
		t.Fatalf("round trip changed the version: %+v", got)
	}
}

func TestSplitV1DocumentConvertsWithBlocks(t *testing.T) {
	meta := validBlocksMeta()
	encoded, err := yaml.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeManifest(encoded)
	if err != nil {
		t.Fatal(err)
	}
	root := got.Images[0]
	if !reflect.DeepEqual(root.Blocks, meta.Blocks) {
		t.Errorf("blocks = %+v, want %+v", root.Blocks, meta.Blocks)
	}
	if root.ID != meta.ID || root.SHA256 != meta.SHA256 ||
		root.Size != meta.Size {
		t.Errorf("identity changed on conversion: %+v", got)
	}
}

func TestSingleFileManifestOmitsSplitKeys(t *testing.T) {
	manifest := validManifest()
	manifest.Images = manifest.Images[:1]
	manifest.Head = manifest.Images[0].ID
	encoded, err := EncodeManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, key := range []string{"blocks:", "members:", "source:", "chain:"} {
		if strings.Contains(text, key) {
			t.Fatalf("single-file manifest grew %s:\n%s", key, text)
		}
	}
	got, err := DecodeManifest(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Images[0].Blocks) != 0 {
		t.Fatalf("split manifests appeared: %+v", got)
	}
}

// testID expands a legacy 16-hex token into the stored 64-hex id.
// A value that is already 64 hex is returned unchanged.
func testID(id string) string {
	if len(id) == idHexLen && isLowerHex(id, idHexLen) {
		return strings.Repeat(id, sumHexLen/idHexLen)
	}
	return id
}

func validMeta() metaV1 {
	const sum = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	return metaV1{
		ID:        sum,
		Name:      "alice/app",
		Version:   MetaVersion,
		Kind:      KindFull,
		Format:    Format{Type: FormatSquashfs, Compression: CompressionZstd},
		Size:      12345,
		SHA256:    sum,
		MtimeUnix: 1710000000,
		CreatedAt: time.Date(2026, 10, 16, 8, 0, 0, 0, time.UTC),
		Builder:   "dev",
	}
}

func validEntry(parent, id string) chainEntryV1 {
	sum := testID(id)
	return chainEntryV1{
		ID:        sum,
		Parent:    testID(parent),
		Size:      4096,
		SHA256:    sum,
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
		meta.Chain = []chainEntryV1{validEntry(meta.ID, "fedcba9876543210")}
		if err := meta.validate(); err != nil {
			t.Errorf("%s/%s: %v", format.Type, format.Compression, err)
		}
	}
}

func TestValidateRejectsDisallowedCompression(t *testing.T) {
	meta := validMeta()
	meta.Format = Format{Type: FormatErofs, Compression: "xz"}
	err := meta.validate()
	if err == nil || !strings.Contains(err.Error(), `format.compression: erofs does not allow "xz"`) {
		t.Fatalf("erofs xz: %v", err)
	}
	meta.Format = Format{Type: FormatSquashfs, Compression: ""}
	err = meta.validate()
	if err == nil || !strings.Contains(err.Error(), `format.compression: squashfs does not allow ""`) {
		t.Fatalf("empty compression: %v", err)
	}
}

// validManifest is a three-node linear DAG: one full root and two
// incrementals, headed by the deepest tip.
func validManifest() Manifest {
	const root = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	one := testID("fedcba9876543210")
	two := testID("0011223344556677")
	return Manifest{
		Version: ManifestVersion,
		Name:    "alice/app",
		Format:  Format{Type: FormatSquashfs, Compression: CompressionZstd},
		Head:    two,
		Images: []ImageMeta{
			{
				ID:        root,
				Kind:      KindFull,
				Size:      12345,
				SHA256:    root,
				MtimeUnix: 1710000000,
				CreatedAt: time.Date(2026, 10, 16, 8, 0, 0, 0, time.UTC),
				Builder:   "dev",
			},
			{
				ID:        one,
				Kind:      KindIncremental,
				Parent:    root,
				Size:      4096,
				SHA256:    one,
				MtimeUnix: 1710000001,
				CreatedAt: time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC),
			},
			{
				ID:        two,
				Kind:      KindIncremental,
				Parent:    one,
				Size:      4096,
				SHA256:    two,
				MtimeUnix: 1710000002,
				CreatedAt: time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC),
			},
		},
	}
}

// deepManifest is a linear manifest of one full root plus n
// incremental layers, headed by the deepest tip.
func deepManifest(n int) Manifest {
	manifest := validManifest()
	manifest.Images = manifest.Images[:1]
	parent := manifest.Images[0].ID
	for i := range n {
		id := testID(fmt.Sprintf("%016x", i+1))
		manifest.Images = append(manifest.Images, ImageMeta{
			ID:        id,
			Kind:      KindIncremental,
			Parent:    parent,
			Size:      4096,
			SHA256:    id,
			MtimeUnix: int64(1710000001 + i),
			CreatedAt: time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC),
		})
		parent = id
	}
	manifest.Head = parent
	return manifest
}

// storeV1 writes the legacy v1 encoding of meta and returns the
// path. It feeds the loader-migration tests a document shaped the
// way old builds left on disk.
func storeV1(t *testing.T, meta metaV1) string {
	t.Helper()
	body, err := yaml.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "meta.yaml")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadManifestConvertsV1ChainToLinearDAG(t *testing.T) {
	meta := validMeta()
	meta.Chain = []chainEntryV1{
		validEntry(meta.ID, "fedcba9876543210"),
		validEntry("fedcba9876543210", "0011223344556677"),
	}
	got, err := LoadManifest(storeV1(t, meta))
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != ManifestVersion {
		t.Errorf("version = %q, want %q", got.Version, ManifestVersion)
	}
	if got.Name != meta.Name || got.Format != meta.Format {
		t.Errorf("header changed: %+v", got)
	}
	if len(got.Images) != 1+len(meta.Chain) {
		t.Fatalf("images = %d, want %d", len(got.Images), 1+len(meta.Chain))
	}
	root := got.Images[0]
	if root.Kind != KindFull || root.ID != meta.ID || root.Parent != "" {
		t.Errorf("root = %+v", root)
	}
	if root.Size != meta.Size || root.SHA256 != meta.SHA256 ||
		root.MtimeUnix != meta.MtimeUnix || root.Builder != meta.Builder ||
		!root.CreatedAt.Equal(meta.CreatedAt) || root.Deprecated {
		t.Errorf("root facts changed: %+v", root)
	}
	for i, entry := range meta.Chain {
		node := got.Images[i+1]
		if node.Kind != KindIncremental || node.ID != entry.ID ||
			node.Parent != entry.Parent {
			t.Errorf("images[%d] = %+v, want parent %q", i+1, node, entry.Parent)
		}
		if node.Size != entry.Size || node.SHA256 != entry.SHA256 ||
			node.MtimeUnix != entry.MtimeUnix ||
			!node.CreatedAt.Equal(entry.CreatedAt) {
			t.Errorf("images[%d] facts changed: %+v", i+1, node)
		}
	}
	tail := meta.Chain[len(meta.Chain)-1].ID
	if got.Head != tail {
		t.Errorf("head = %q, want the chain tail %q", got.Head, tail)
	}
	stack, err := got.HeadStack()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{meta.ID, meta.Chain[0].ID, meta.Chain[1].ID}
	if len(stack) != len(want) {
		t.Fatalf("head stack = %+v, want %v", stack, want)
	}
	for i, id := range want {
		if stack[i].ID != id {
			t.Errorf("stack[%d] = %q, want %q, root first", i, stack[i].ID, id)
		}
	}
}

func TestLoadManifestConvertsV1EmptyChain(t *testing.T) {
	meta := validMeta()
	got, err := LoadManifest(storeV1(t, meta))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Images) != 1 || got.Head != meta.ID {
		t.Fatalf("manifest = %+v, want one full node headed by %q", got, meta.ID)
	}
	stack, err := got.HeadStack()
	if err != nil {
		t.Fatal(err)
	}
	if len(stack) != 1 || stack[0].ID != meta.ID || stack[0].Kind != KindFull {
		t.Fatalf("head stack = %+v", stack)
	}
	if _, err := got.Full(); err != nil {
		t.Fatalf("Full: %v", err)
	}
}

func TestLoadManifestCarriesV1SplitPayloadFacts(t *testing.T) {
	meta := validBlocksMeta()
	meta.Deprecated = true
	meta.ContentSHA256 = strings.Repeat("cd", 32)
	meta.Chain = []chainEntryV1{validEntry(meta.ID, "fedcba9876543210")}
	meta.Chain[0].Blocks = blocksSeq(2)
	got, err := LoadManifest(storeV1(t, meta))
	if err != nil {
		t.Fatal(err)
	}
	root := got.Images[0]
	if !root.Deprecated || root.ContentSHA256 != meta.ContentSHA256 {
		t.Errorf("root flags = %+v", root)
	}
	if !reflect.DeepEqual(root.Blocks, meta.Blocks) {
		t.Errorf("root blocks = %+v, want %+v", root.Blocks, meta.Blocks)
	}
	if !reflect.DeepEqual(got.Images[1].Blocks, meta.Chain[0].Blocks) {
		t.Errorf("layer blocks = %+v", got.Images[1].Blocks)
	}
}

func TestManifestV2RoundTrip(t *testing.T) {
	manifest := validManifest()
	encoded, err := EncodeManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `version: "2.0"`) {
		t.Fatalf("encoded document is not v2:\n%s", encoded)
	}
	got, err := DecodeManifest(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, manifest) {
		t.Fatalf("round trip changed the manifest:\n%+v\n%+v", got, manifest)
	}
	path := filepath.Join(t.TempDir(), "meta.yaml")
	if err := os.WriteFile(path, encoded, 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, manifest) {
		t.Fatalf("loaded = %+v", loaded)
	}
}

func TestEncodeManifestWritesOnlyV2(t *testing.T) {
	manifest := validManifest()
	manifest.Version = MetaVersion
	encoded, err := EncodeManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `version: "2.0"`) {
		t.Fatalf("encoded document is not v2:\n%s", encoded)
	}
	got, err := DecodeManifest(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != ManifestVersion {
		t.Fatalf("version = %q, want %q", got.Version, ManifestVersion)
	}
}

func TestLoadManifestRejectsUnknownVersion(t *testing.T) {
	for i, version := range []string{"1", "0.9", "2", "3.0"} {
		body := fmt.Sprintf("version: %q\nname: alice/app\n", version)
		path := filepath.Join(t.TempDir(), fmt.Sprintf("meta%d.yaml", i))
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := LoadManifest(path)
		if err == nil {
			t.Fatalf("version %s accepted", version)
		}
		if want := "unsupported metadata version"; !strings.Contains(err.Error(), want) {
			t.Fatalf("version %s: error = %v", version, err)
		}
	}
	// A versionless document reads as the pre-version v1 layout, so
	// a foreign body fails closed on the v1 shape rules instead.
	path := filepath.Join(t.TempDir(), "no-version.yaml")
	if err := os.WriteFile(path, []byte("name: alice/app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadManifest(path); err == nil ||
		!strings.Contains(err.Error(), "id: must be 64 lowercase hex digits") {
		t.Fatalf("missing version: error = %v", err)
	}
	// An empty document still fails closed.
	empty := filepath.Join(t.TempDir(), "empty.yaml")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadManifest(empty); err == nil ||
		!strings.Contains(err.Error(), "empty document") {
		t.Fatalf("empty document: error = %v", err)
	}
}

func TestLoadManifestAcceptsPreVersionDocuments(t *testing.T) {
	// Builds predating the reserved version field left it empty
	// while writing the v1 layout. Both the empty spelling and a
	// document without the key load as v1, and a rewrite upgrades
	// them to the DAG layout.
	meta := validMeta()
	meta.Version = ""
	meta.Chain = []chainEntryV1{validEntry(meta.ID, "fedcba9876543210")}
	body, err := yaml.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	stripped := strings.Replace(string(body), "version: \"\"\n", "", 1)
	if stripped == string(body) || strings.Contains(stripped, "version:") {
		t.Fatalf("fixture does not exercise the missing key:\n%s", stripped)
	}
	tail := meta.Chain[0].ID
	tests := []struct {
		name string
		body []byte
	}{
		{"empty version", body},
		{"missing version key", []byte(stripped)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "meta.yaml")
			if err := os.WriteFile(path, tt.body, 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := LoadManifest(path)
			if err != nil {
				t.Fatal(err)
			}
			if got.Version != ManifestVersion {
				t.Errorf("version = %q, want %q", got.Version, ManifestVersion)
			}
			if len(got.Images) != 2 || got.Head != tail {
				t.Fatalf("manifest = %+v, want the chain tail %q as head", got, tail)
			}
			upgraded, err := EncodeManifest(got)
			if err != nil {
				t.Fatal(err)
			}
			version, err := probeDocVersion(upgraded)
			if err != nil || version != ManifestVersion {
				t.Fatalf("rewrite probes as %q, %v", version, err)
			}
		})
	}
}

func TestDecodeManifestRejectsBrokenDocuments(t *testing.T) {
	duplicate := validManifest()
	duplicate.Images[2].ID = duplicate.Images[1].ID
	duplicateBody, err := yaml.Marshal(duplicate)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		body []byte
		want string
	}{
		{
			name: "malformed YAML",
			body: []byte("version: [unterminated\n"),
			want: "yaml:",
		},
		{
			name: "invalid v1 document",
			body: []byte("version: \"1.0\"\nname: alice/app\nkind: full\n"),
			want: "id: must be 64 lowercase hex digits",
		},
		{
			name: "malformed v2 body",
			body: []byte("version: \"2.0\"\nname: alice/app\nimages: 42\n"),
			want: "yaml: unmarshal errors",
		},
		{
			name: "v2 document failing validation",
			body: duplicateBody,
			want: "images[2].id: duplicate",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := DecodeManifest(tt.body); err == nil ||
				!strings.Contains(err.Error(), tt.want) {
				t.Fatalf("DecodeManifest error = %v, want %q", err, tt.want)
			}
			path := filepath.Join(t.TempDir(), "meta.yaml")
			if err := os.WriteFile(path, tt.body, 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadManifest(path); err == nil ||
				!strings.Contains(err.Error(), tt.want) {
				t.Fatalf("LoadManifest error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestEncodeManifestRejectsInvalidManifest(t *testing.T) {
	manifest := validManifest()
	manifest.Head = ""
	if _, err := EncodeManifest(manifest); err == nil ||
		!strings.Contains(err.Error(), "head: must be set") {
		t.Fatalf("error = %v, want a head violation", err)
	}
}

func TestLoadManifestReportsReadFailure(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.yaml")
	if _, err := LoadManifest(missing); err == nil ||
		!errors.Is(err, os.ErrNotExist) {
		t.Fatalf("error = %v, want a missing-file error", err)
	}
}

func TestManifestGraphQueries(t *testing.T) {
	manifest := validManifest()
	root := manifest.Images[0].ID
	one := manifest.Images[1].ID
	two := manifest.Images[2].ID
	unknown := strings.Repeat("e", sumHexLen)

	full, err := manifest.Full()
	if err != nil || full.ID != root {
		t.Fatalf("Full = %+v, %v", full, err)
	}
	if node, ok := manifest.Node(one); !ok || node.Parent != root {
		t.Fatalf("Node(%q) = %+v, %v", one, node, ok)
	}
	if !manifest.Has(two) || manifest.Has(unknown) {
		t.Fatal("Has misreported")
	}
	stack, err := manifest.Stack(one)
	if err != nil {
		t.Fatal(err)
	}
	if len(stack) != 2 || stack[0].ID != root || stack[1].ID != one {
		t.Fatalf("Stack(%q) = %+v, want the root first", one, stack)
	}
	if head, err := manifest.HeadStack(); err != nil || len(head) != 3 {
		t.Fatalf("HeadStack = %+v, %v", head, err)
	}
	if _, err := manifest.Stack(unknown); err == nil ||
		!strings.Contains(err.Error(), "unknown image id") {
		t.Fatalf("unknown tip: %v", err)
	}
	if _, err := manifest.Stack(""); err == nil {
		t.Fatal("empty tip accepted")
	}
	seen := manifest.Reachable(one)
	if !seen[root] || !seen[one] || seen[two] {
		t.Fatalf("Reachable(%q) = %v", one, seen)
	}
	seen = manifest.Reachable(two, unknown)
	if len(seen) != 3 || !seen[root] || !seen[one] || !seen[two] {
		t.Fatalf("Reachable with an unknown tip = %v", seen)
	}
}

func TestManifestStackRejectsBrokenGraphs(t *testing.T) {
	orphan := validManifest()
	orphan.Images = orphan.Images[1:]
	orphan.Images[0].Parent = ""
	if _, err := orphan.Stack(orphan.Images[1].ID); err == nil ||
		!strings.Contains(err.Error(), "does not start at the full root") {
		t.Fatalf("orphan stack: %v", err)
	}
	loop := validManifest()
	loop.Images[1].Parent = loop.Images[2].ID
	if _, err := loop.Stack(loop.Images[1].ID); err == nil ||
		!strings.Contains(err.Error(), "parent cycle") {
		t.Fatalf("cycle: %v", err)
	}
}

func TestManifestFullErrors(t *testing.T) {
	noRoot := validManifest()
	noRoot.Images = noRoot.Images[1:]
	if _, err := noRoot.Full(); err == nil ||
		!strings.Contains(err.Error(), "no full root") {
		t.Fatalf("no root: %v", err)
	}
	twoRoots := validManifest()
	twoRoots.Images[1].Kind = KindFull
	if _, err := twoRoots.Full(); err == nil ||
		!strings.Contains(err.Error(), "more than one full root") {
		t.Fatalf("two roots: %v", err)
	}
}

func TestManifestValidateReportsEveryBranch(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Manifest)
		want   string
	}{
		{
			name:   "duplicate id",
			mutate: func(m *Manifest) { m.Images[2].ID = m.Images[1].ID },
			want:   "images[2].id: duplicate",
		},
		{
			name:   "malformed id",
			mutate: func(m *Manifest) { m.Images[1].ID = strings.Repeat("E", sumHexLen) },
			want:   "images[1].id: must be 64 lowercase hex digits",
		},
		{
			name:   "unknown parent",
			mutate: func(m *Manifest) { m.Images[1].Parent = strings.Repeat("e", sumHexLen) },
			want:   "images[1].parent: unknown image id",
		},
		{
			name:   "incremental without parent",
			mutate: func(m *Manifest) { m.Images[1].Parent = "" },
			want:   "images[1].parent: incremental nodes must have a parent",
		},
		{
			name: "cycle",
			mutate: func(m *Manifest) {
				m.Images[1].Parent = m.Images[2].ID
			},
			want: "images[1].parent: the parent edges must stay acyclic",
		},
		{
			name: "two full roots",
			mutate: func(m *Manifest) {
				m.Images[1].Kind = KindFull
				m.Images[1].Parent = ""
			},
			want: "images[1].kind: more than one full root",
		},
		{
			name:   "no full root",
			mutate: func(m *Manifest) { m.Images[0].Kind = KindIncremental },
			want:   "images: must hold exactly one full root",
		},
		{
			name:   "root with parent",
			mutate: func(m *Manifest) { m.Images[0].Parent = m.Images[1].ID },
			want:   "images[0].parent: the full root must not have a parent",
		},
		{
			name:   "unknown head",
			mutate: func(m *Manifest) { m.Head = strings.Repeat("d", sumHexLen) },
			want:   "head: unknown image id",
		},
		{
			name:   "empty head",
			mutate: func(m *Manifest) { m.Head = "" },
			want:   "head: must be set",
		},
		{
			name:   "blocks on a single payload layer",
			mutate: func(m *Manifest) { m.Images[1].Blocks = blocksSeq(1) },
			want:   "images[1].blocks: must be empty for single payloads",
		},
		{
			name: "blocks missing on a split payload layer",
			mutate: func(m *Manifest) {
				m.Images[0].Blocks = blocksSeq(2)
			},
			want: "images[1].blocks: must be set for blocks payloads",
		},
		{
			name:   "old version",
			mutate: func(m *Manifest) { m.Version = MetaVersion },
			want:   `version: unsupported metadata version "1.0"`,
		},
		{
			name:   "bad name",
			mutate: func(m *Manifest) { m.Name = "alice//app" },
			want:   "name: invalid image name",
		},
		{
			name:   "bad format",
			mutate: func(m *Manifest) { m.Format.Type = "tar" },
			want:   `format.type: unknown container type: "tar"`,
		},
		{
			name:   "invalid kind",
			mutate: func(m *Manifest) { m.Images[1].Kind = "layer" },
			want:   "images[1].kind: must be full or incremental",
		},
		{
			name:   "malformed parent id",
			mutate: func(m *Manifest) { m.Images[1].Parent = "xyz" },
			want: "images[1].parent: must be 64 lowercase hex " +
				"digits, or a legacy 16-digit id",
		},
		{
			name:   "digest mismatch",
			mutate: func(m *Manifest) { m.Images[1].SHA256 = strings.Repeat("9", sumHexLen) },
			want:   "images[1].id: must equal sha256",
		},
		{
			name:   "malformed digest",
			mutate: func(m *Manifest) { m.Images[1].SHA256 = "not-hex" },
			want: "images[1].sha256: must be 64 lowercase hex " +
				"digits, or a legacy 16-digit id",
		},
		{
			name:   "zero size",
			mutate: func(m *Manifest) { m.Images[1].Size = 0 },
			want:   "images[1].size: must be greater than 0",
		},
		{
			name:   "negative mtime",
			mutate: func(m *Manifest) { m.Images[1].MtimeUnix = -1 },
			want:   "images[1].mtime_unix: must not be negative",
		},
		{
			name:   "missing created_at",
			mutate: func(m *Manifest) { m.Images[1].CreatedAt = time.Time{} },
			want:   "images[1].created_at: must be set",
		},
		{
			name: "non-UTC created_at",
			mutate: func(m *Manifest) {
				m.Images[1].CreatedAt = time.Date(2026, 10, 16, 9, 0, 0, 0,
					time.FixedZone("CET", 3600))
			},
			want: "images[1].created_at: must be UTC",
		},
		{
			name:   "root without builder",
			mutate: func(m *Manifest) { m.Images[0].Builder = "" },
			want:   "images[0].builder: must be set",
		},
		{
			name:   "malformed root content digest",
			mutate: func(m *Manifest) { m.Images[0].ContentSHA256 = strings.Repeat("z", sumHexLen) },
			want:   "images[0].content_sha256: must be 64 lowercase hex digits",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifest := validManifest()
			tt.mutate(&manifest)
			err := manifest.Validate()
			if err == nil {
				t.Fatalf("manifest accepted, want %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
	if err := validManifest().Validate(); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
}

func TestManifestValidateEnforcesStackDepth(t *testing.T) {
	atLimit := deepManifest(maxStackDepth - 1)
	if err := atLimit.Validate(); err != nil {
		t.Fatalf("depth %d rejected: %v", maxStackDepth, err)
	}
	stack, err := atLimit.HeadStack()
	if err != nil {
		t.Fatal(err)
	}
	if len(stack) != maxStackDepth {
		t.Fatalf("stack = %d, want %d", len(stack), maxStackDepth)
	}
	over := deepManifest(maxStackDepth)
	err = over.Validate()
	if err == nil {
		t.Fatal("over-deep manifest accepted")
	}
	if !strings.Contains(err.Error(), "exceeds the stack limit of 8") {
		t.Fatalf("error = %v", err)
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("images[%d]:", maxStackDepth)) {
		t.Fatalf("deepest node not named: %v", err)
	}
}

func TestManifestValidateAcceptsLegacyIDs(t *testing.T) {
	full := "0123456789abcdef"
	tip := "fedcba9876543210"
	manifest := Manifest{
		Version: ManifestVersion,
		Name:    "alice/app",
		Format:  Format{Type: FormatSquashfs, Compression: CompressionZstd},
		Head:    tip,
		Images: []ImageMeta{
			{
				ID:        full,
				Kind:      KindFull,
				Size:      12345,
				SHA256:    full,
				MtimeUnix: 1710000000,
				CreatedAt: time.Date(2026, 10, 16, 8, 0, 0, 0, time.UTC),
				Builder:   "dev",
			},
			{
				ID:        tip,
				Kind:      KindIncremental,
				Parent:    full,
				Size:      4096,
				SHA256:    tip,
				MtimeUnix: 1710000001,
				CreatedAt: time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC),
			},
		},
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("legacy ids rejected: %v", err)
	}
	stack, err := manifest.HeadStack()
	if err != nil {
		t.Fatal(err)
	}
	if len(stack) != 2 || stack[0].ID != full || stack[1].ID != tip {
		t.Fatalf("head stack = %+v", stack)
	}
	bad := manifest
	bad.Images[1].ID = "0123456789abcde"
	if err := bad.Validate(); err == nil ||
		!strings.Contains(err.Error(), "images[1].id: must be 64 lowercase hex digits") {
		t.Fatalf("short id: %v", err)
	}
}

// validManifestWithBranch is validManifest plus one branch node
// parented on the root. The head stays on the linear tip, so the
// branch node is off the head stack. It returns the manifest and the
// branch node's id.
func validManifestWithBranch() (Manifest, string) {
	manifest := validManifest()
	branch := testID("beefbeefbeefbeef")
	manifest.Images = append(manifest.Images, ImageMeta{
		ID:        branch,
		Kind:      KindIncremental,
		Parent:    manifest.Images[0].ID,
		Size:      4096,
		SHA256:    branch,
		MtimeUnix: 1710000003,
		CreatedAt: time.Date(2026, 10, 16, 11, 0, 0, 0, time.UTC),
	})
	return manifest, branch
}

func TestManifestBranchesCountsOffHeadNodes(t *testing.T) {
	manifest := validManifest()
	if got := manifest.Branches(); got != 0 {
		t.Fatalf("linear manifest branches = %d", got)
	}
	withBranch, branch := validManifestWithBranch()
	if got := withBranch.Branches(); got != 1 {
		t.Fatalf("branches = %d, want 1", got)
	}
	// Moving the head onto the branch leaves the two linear nodes
	// off the head stack.
	reheaded := withBranch
	reheaded.Head = branch
	if got := reheaded.Branches(); got != 2 {
		t.Fatalf("reheaded branches = %d, want 2", got)
	}
}
