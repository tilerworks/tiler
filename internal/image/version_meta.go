package image

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"gopkg.in/yaml.v3"
)

// VersionManifestVersion identifies immutable, independently addressable versions.
const VersionManifestVersion = "3.0"

// VersionManifest stores multiple full roots and their single-parent descendants.
// Runtime readers continue to use Manifest until the new layout is activated.
type VersionManifest struct {
	Version string      `yaml:"version"`
	Name    string      `yaml:"name"`
	Images  []ImageMeta `yaml:"images"`
}

// Validate checks v3 payload facts without imposing a shared root or format.
func (m VersionManifest) Validate() error {
	var list issueList
	if m.Version != VersionManifestVersion {
		list.add("version: unsupported metadata version %q", m.Version)
	}
	checkNameField(m.Name, &list)
	for i := range m.Images {
		node := &m.Images[i]
		prefix := fmt.Sprintf("images[%d]", i)
		checkDigest(node.ID, node.SHA256, prefix, &list)
		checkNodeFacts(prefix, node, &list)
		checkContainer(node.Format, &list)
		checkBlockList(prefix+".blocks", node.Blocks, &list)
		if node.Deprecated {
			list.add("%s.deprecated: unsupported lifecycle state", prefix)
		}
	}
	if _, err := NewForest(m.Images); err != nil {
		list.add("images: %v", err)
	}
	return list.err()
}

// DecodeVersionManifest accepts exactly one v3 document, never upgrading old data.
func DecodeVersionManifest(data []byte) (VersionManifest, error) {
	var header struct {
		Version string                 `yaml:"version"`
		Images  []map[string]yaml.Node `yaml:"images"`
	}
	if err := yaml.Unmarshal(data, &header); err != nil {
		return VersionManifest{}, err
	}
	if header.Version != VersionManifestVersion {
		return VersionManifest{}, fmt.Errorf("unsupported metadata version %q; rebuild images and sessions", header.Version)
	}
	for _, node := range header.Images {
		if _, legacy := node["deprecated"]; legacy {
			return VersionManifest{}, errors.New("deprecated: unsupported lifecycle state")
		}
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var manifest VersionManifest
	if err := dec.Decode(&manifest); err != nil {
		return VersionManifest{}, err
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return VersionManifest{}, errors.New("metadata must contain exactly one document")
	}
	if err := manifest.Validate(); err != nil {
		return VersionManifest{}, err
	}
	return manifest, nil
}

// EncodeVersionManifest validates and encodes v3 without legacy lifecycle fields.
func EncodeVersionManifest(manifest VersionManifest) ([]byte, error) {
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	return yaml.Marshal(manifest)
}
