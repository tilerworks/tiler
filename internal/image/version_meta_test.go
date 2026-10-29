package image

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func versionFixture() VersionManifest {
	old := validManifest()
	for i := range old.Images {
		old.Images[i].Format = old.Format
	}
	old.Images[1].Format = Format{Type: FormatErofs, Compression: "lz4"}
	old.Images[1].Description = "Development tools\n\nCompiler and debugger."
	root := old.Images[0]
	root.ID, root.SHA256 = strings.Repeat("a", 64), strings.Repeat("a", 64)
	return VersionManifest{Version: VersionManifestVersion, Name: old.Name, Images: append(old.Images, root)}
}

func TestVersionManifestRoundTrip(t *testing.T) {
	for _, want := range []VersionManifest{versionFixture(), {Version: VersionManifestVersion, Name: "empty"}} {
		data, err := EncodeVersionManifest(want)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "deprecated:") || strings.Contains(string(data), "head:") {
			t.Fatalf("legacy fields in v3: %s", data)
		}
		got, err := DecodeVersionManifest(data)
		if err != nil || got.Name != want.Name || len(got.Images) != len(want.Images) {
			t.Fatalf("decode: %#v, %v", got, err)
		}
		if len(want.Images) > 0 && !reflect.DeepEqual(got, want) {
			t.Fatalf("round trip changed node metadata: %#v", got)
		}
	}
}

func TestVersionManifestRejectsInvalidFacts(t *testing.T) {
	for _, edit := range []func(*VersionManifest){
		func(m *VersionManifest) { m.Version = 2 },
		func(m *VersionManifest) { m.Name = "bad:name" },
		func(m *VersionManifest) { m.Images[0].ID = strings.Repeat("a", 16) },
		func(m *VersionManifest) { m.Images[0].SHA256 = strings.Repeat("b", 64) },
		func(m *VersionManifest) { m.Images[0].CreatedAt = time.Time{} },
		func(m *VersionManifest) { m.Images[0].Format = Format{} },
		func(m *VersionManifest) { m.Images[0].Deprecated = true },
		func(m *VersionManifest) { m.Images[0].Blocks = []BlockEntry{{Name: "bad"}} },
		func(m *VersionManifest) { m.Images[0].Parent = m.Images[1].ID },
		func(m *VersionManifest) { m.Images[1].Parent = strings.Repeat("b", 64) },
		func(m *VersionManifest) { m.Images = append(m.Images, m.Images[0]) },
	} {
		m := versionFixture()
		edit(&m)
		if _, err := EncodeVersionManifest(m); err == nil {
			t.Fatalf("accepted invalid metadata: %#v", m)
		}
	}
}

func TestVersionManifestRejectsLegacyAndMixedSchemas(t *testing.T) {
	data, err := EncodeVersionManifest(versionFixture())
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		"", "version: '1.0'", "version: '2.0'", "version: '4.0'", "name: old/app", "[",
		string(data) + "head: old\n", string(data) + "format: {type: squashfs}\n",
		string(data) + "---\n{}\n",
		strings.Replace(string(data), "kind: full", "kind: full\n      deprecated: false", 1),
		strings.Replace(string(data), "kind: full", "kind: full\n      deprecated: null", 1),
		strings.Replace(string(data), "type: erofs", "type: unknown", 1),
	} {
		if _, err := DecodeVersionManifest([]byte(bad)); err == nil {
			t.Fatalf("accepted invalid document: %s", bad)
		}
	}
}
