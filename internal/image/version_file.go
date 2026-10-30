package image

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// fileObjectVersion is the redundant node document beside a payload.
const fileObjectVersion = "image.yaml"

// maxObjectVersionFile bounds one image.yaml. A larger file fails
// without scanning the rest.
const maxObjectVersionFile = 8 << 20

// ErrVersionFile is a present image.yaml that is empty, malformed,
// oversized, or not a regular file. A missing file is not this error.
var ErrVersionFile = errors.New("invalid version metadata")

// ObjectVersionFile is one version's redundant copy of its meta.yaml
// node. The parent does not have to appear in the same file.
type ObjectVersionFile struct {
	Version int       `yaml:"version"`
	Name    string    `yaml:"name"`
	Image   ImageMeta `yaml:"image"`
}

// EncodeObjectVersionFile validates and encodes one version file.
func EncodeObjectVersionFile(file ObjectVersionFile) ([]byte, error) {
	if err := file.Validate(); err != nil {
		return nil, err
	}
	return encodeTimestampDocument(file)
}

// DecodeObjectVersionFile accepts exactly one version document.
func DecodeObjectVersionFile(data []byte) (ObjectVersionFile, error) {
	if len(data) > maxObjectVersionFile {
		return ObjectVersionFile{}, fmt.Errorf("%w: exceeds %d bytes", ErrVersionFile, maxObjectVersionFile)
	}
	var header struct {
		Version any                  `yaml:"version"`
		Image   map[string]yaml.Node `yaml:"image"`
	}
	if err := yaml.Unmarshal(data, &header); err != nil {
		return ObjectVersionFile{}, err
	}
	if version, ok := header.Version.(int); !ok || version != VersionManifestVersion {
		return ObjectVersionFile{}, fmt.Errorf("unsupported metadata version %v; rebuild images and sessions", header.Version)
	}
	if _, legacy := header.Image["deprecated"]; legacy {
		return ObjectVersionFile{}, errors.New("deprecated: unsupported lifecycle state")
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var file ObjectVersionFile
	if err := dec.Decode(&file); err != nil {
		return ObjectVersionFile{}, err
	}
	file.Image.CreatedAt = normalizeCreatedAt(file.Image.CreatedAt)
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return ObjectVersionFile{}, errors.New("metadata must contain exactly one document")
	}
	if err := file.Validate(); err != nil {
		return ObjectVersionFile{}, err
	}
	return file, nil
}

// Validate checks one version file. The parent may live in another file.
func (file ObjectVersionFile) Validate() error {
	var list issueList
	if file.Version != VersionManifestVersion {
		list.add("version: unsupported metadata version %d", file.Version)
	}
	checkNameField(file.Name, &list)
	node := &file.Image
	const prefix = "image"
	if node.Kind != KindFull && node.Kind != KindIncremental {
		list.add("%s.kind: must be full or incremental", prefix)
	}
	switch {
	case node.Kind == KindFull && node.Parent != "":
		list.add("%s.parent: the full root must not have a parent", prefix)
	case node.Kind == KindIncremental && node.Parent == "":
		list.add("%s.parent: incremental nodes must have a parent", prefix)
	case node.Parent != "" && !onDiskID(node.Parent):
		list.add("%s.parent: must be %d lowercase hex digits, or a legacy %d-digit id",
			prefix, sumHexLen, idHexLen)
	}
	checkDigest(node.ID, node.SHA256, prefix, &list)
	checkNodeFacts(prefix, node, &list)
	checkContainer(node.Format, &list)
	checkBlockList(prefix+".blocks", node.Blocks, &list)
	if node.Deprecated {
		list.add("%s.deprecated: unsupported lifecycle state", prefix)
	}
	return list.err()
}

func (repo Repo) objectVersionFilePath(id string) (string, error) {
	return repo.objectPath(id, fileObjectVersion)
}

func syncObjectVersionFiles(repo Repo, manifest VersionManifest, seam hooks) error {
	for _, node := range manifest.Images {
		if err := writeObjectVersionFile(repo, node, seam); err != nil {
			return err
		}
	}
	return nil
}

func writeObjectVersionFile(repo Repo, node ImageMeta, seam hooks) error {
	path, err := repo.objectVersionFilePath(node.ID)
	if err != nil {
		return err
	}
	body, err := EncodeObjectVersionFile(ObjectVersionFile{
		Version: VersionManifestVersion, Name: repo.Name(), Image: node,
	})
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := writeTemp(dir, fileObjectVersion, body, hooks{})
	if err != nil {
		return err
	}
	defer removeFile(tmp)
	return seam.do(tmp, path)
}

func restoreObjectVersionFiles(repo Repo, prior *VersionManifest, next VersionManifest) error {
	keep := make(map[string]bool, len(next.Images))
	var err error
	if prior != nil {
		for _, node := range prior.Images {
			keep[node.ID] = true
			err = errors.Join(err, writeObjectVersionFile(repo, node, hooks{}))
		}
	}
	for _, node := range next.Images {
		if keep[node.ID] {
			continue
		}
		err = errors.Join(err, removeObjectVersionFile(repo, node.ID))
	}
	return err
}

func removeObjectVersionFile(repo Repo, id string) error {
	path, err := repo.objectVersionFilePath(id)
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// ensureObjectVersionFile rewrites image.yaml when the stored bytes
// differ from the committed node. A matching file is left in place.
func ensureObjectVersionFile(repo Repo, node ImageMeta) error {
	path, err := repo.objectVersionFilePath(node.ID)
	if err != nil {
		return err
	}
	want, err := EncodeObjectVersionFile(ObjectVersionFile{
		Version: VersionManifestVersion, Name: repo.Name(), Image: node,
	})
	if err != nil {
		return err
	}
	got, err := os.ReadFile(path)
	if err == nil && bytes.Equal(got, want) {
		return nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return writeObjectVersionFile(repo, node, hooks{})
}
