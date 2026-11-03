package image

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	"gopkg.in/yaml.v3"
)

// VersionManifestVersion is the only metadata schema. tiler has not
// shipped, so the baseline is the integer 1.
const VersionManifestVersion = 1

// VersionManifest stores multiple full roots and their single-parent descendants.
type VersionManifest struct {
	Version int         `yaml:"version"`
	Name    string      `yaml:"name"`
	Images  []ImageMeta `yaml:"images"`
}

// LoadVersionManifest reads one committed v3 snapshot, never creating metadata.
// Callers coordinate recovery and subsequent payload I/O with the library lock.
func LoadVersionManifest(repo Repo) (VersionManifest, error) {
	manifest, body, err := loadVersionMeta(repo)
	if err != nil {
		return manifest, fmt.Errorf("image %q: read metadata %q: %w", repo.Name(), repo.MetaPath(), err)
	}
	if body == nil {
		_, statErr := os.Stat(repo.Dir)
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return manifest, fmt.Errorf("image %q: inspect repository %q: %w", repo.Name(), repo.Dir, statErr)
		}
		err = &MissingManifestError{Name: repo.Name(), Library: libraryRoot(repo), Path: repo.MetaPath(), RepositoryExists: statErr == nil}
	}
	return manifest, err
}

// Validate checks v3 payload facts without imposing a shared root or format.
func (m VersionManifest) Validate() error {
	var list issueList
	if m.Version != VersionManifestVersion {
		list.add("version: unsupported metadata version %d", m.Version)
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

// DecodeVersionManifest accepts exactly one document at the baseline
// schema, never upgrading an older layout.
func DecodeVersionManifest(data []byte) (VersionManifest, error) {
	var header struct {
		Version any                    `yaml:"version"`
		Images  []map[string]yaml.Node `yaml:"images"`
	}
	if err := yaml.Unmarshal(data, &header); err != nil {
		return VersionManifest{}, err
	}
	if version, ok := header.Version.(int); !ok || version != VersionManifestVersion {
		return VersionManifest{}, fmt.Errorf("unsupported metadata version %v; rebuild images and sessions", header.Version)
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
	normalizeManifestTimes(manifest.Images)
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
	return encodeTimestampDocument(manifest)
}
