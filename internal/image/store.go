package image

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	"gopkg.in/yaml.v3"
)

// Decode reads one metadata document. Unknown fields are discarded,
// then the document is validated.
func Decode(data []byte) (Meta, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var meta Meta
	if err := dec.Decode(&meta); err != nil {
		if errors.Is(err, io.EOF) {
			return Meta{}, errors.New("empty document")
		}
		return Meta{}, err
	}
	if err := meta.Validate(); err != nil {
		return Meta{}, err
	}
	return meta, nil
}

// Encode validates meta and writes YAML. Unknown fields cannot
// reappear because they are not part of Meta.
func Encode(meta Meta) ([]byte, error) {
	if err := meta.Validate(); err != nil {
		return nil, err
	}
	data, err := yaml.Marshal(meta)
	if err != nil {
		return nil, fmt.Errorf("meta: %w", err)
	}
	return data, nil
}

// Load reads a metadata file and validates it.
func Load(path string) (Meta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Meta{}, err
	}
	meta, err := Decode(data)
	if err != nil {
		return Meta{}, fmt.Errorf("meta: %w", err)
	}
	return meta, nil
}

// Store writes validated metadata to path.
func Store(path string, meta Meta) error {
	data, err := Encode(meta)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("meta: %w", err)
	}
	return nil
}

// DecodeManifest reads one metadata document as a manifest DAG. A
// MetaVersion document converts through its linear chain, a
// ManifestVersion document loads as-is, and every other version is
// refused. The result is validated.
func DecodeManifest(data []byte) (Manifest, error) {
	var probe struct {
		Version string `yaml:"version"`
	}
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	switch probe.Version {
	case MetaVersion:
		meta, err := Decode(data)
		if err != nil {
			return Manifest{}, err
		}
		manifest = manifestFromMeta(meta)
	case ManifestVersion:
		if err := yaml.Unmarshal(data, &manifest); err != nil {
			return Manifest{}, err
		}
	default:
		return Manifest{}, fmt.Errorf(
			"unsupported metadata version %q", probe.Version)
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// EncodeManifest validates the manifest and writes YAML. The output
// is always ManifestVersion: no v1 writer exists, so rewriting a
// lazily migrated document upgrades it.
func EncodeManifest(manifest Manifest) ([]byte, error) {
	manifest.Version = ManifestVersion
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	data, err := yaml.Marshal(manifest)
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
