package image

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	"gopkg.in/yaml.v3"
)

// StoreManifest writes a validated manifest to path.
func StoreManifest(path string, manifest Manifest) error {
	data, err := EncodeManifest(manifest)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("meta: %w", err)
	}
	return nil
}

// probeDocVersion reads the version key of a metadata document. A
// malformed YAML body fails here. A missing key probes as the empty
// version, which DecodeManifest reads as a pre-version document in
// the v1 layout.
func probeDocVersion(data []byte) (string, error) {
	var probe struct {
		Version string `yaml:"version"`
	}
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return "", err
	}
	return probe.Version, nil
}

// DecodeManifest reads one metadata document as a manifest DAG. A
// MetaVersion document converts through its linear chain, and so does
// a document without a version: builds predating the reserved field
// left it empty while writing the same v1 layout. A ManifestVersion
// document loads as-is, and every other version is refused. The
// result is validated.
func DecodeManifest(data []byte) (Manifest, error) {
	version, err := probeDocVersion(data)
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	switch version {
	case MetaVersion, "":
		if manifest, err = decodeV1(data); err != nil {
			return Manifest{}, err
		}
	case ManifestVersion:
		if err := yaml.Unmarshal(data, &manifest); err != nil {
			return Manifest{}, err
		}
		normalizeManifestTimes(manifest.Images)
	default:
		return Manifest{}, fmt.Errorf(
			"unsupported metadata version %q", version)
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// decodeV1 reads one legacy v1 document and converts its linear
// chain into a DAG. Documents written before the version field was
// reserved carry an empty version in the same layout, so the empty
// version normalizes to MetaVersion before validation. Unknown fields
// are dropped, then the document is validated against the v1 rules
// before the conversion, so a broken legacy document stays
// fail-closed.
func decodeV1(data []byte) (Manifest, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var meta metaV1
	if err := dec.Decode(&meta); err != nil {
		if errors.Is(err, io.EOF) {
			return Manifest{}, errors.New("empty document")
		}
		return Manifest{}, err
	}
	if meta.Version == "" {
		meta.Version = MetaVersion
	}
	normalizeV1Times(&meta)
	if err := meta.validate(); err != nil {
		return Manifest{}, err
	}
	return manifestFromV1(meta), nil
}

// EncodeManifest validates the manifest and writes YAML. The output
// is always ManifestVersion: no v1 writer exists, so rewriting a
// lazily migrated document upgrades it.
func EncodeManifest(manifest Manifest) ([]byte, error) {
	manifest.Version = ManifestVersion
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	data, err := encodeTimestampDocument(manifest)
	if err != nil {
		return nil, fmt.Errorf("meta: %w", err)
	}
	return data, nil
}

// LoadManifest reads a metadata file as a manifest DAG and
// validates it.
func LoadManifest(path string) (Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	manifest, err := DecodeManifest(data)
	if err != nil {
		return Manifest{}, fmt.Errorf("meta: %w", err)
	}
	return manifest, nil
}
