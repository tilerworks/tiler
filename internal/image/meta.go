package image

import (
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

const (
	// KindFull is a complete image.
	KindFull = "full"
	// KindIncremental is one upper layer appended to the active image.
	KindIncremental = "incremental"

	// FormatSquashfs is the default image container type.
	FormatSquashfs = "squashfs"
	// FormatErofs is the erofs image container type.
	FormatErofs = "erofs"
	// FormatDMG is the macOS disk-image container type. Metadata may
	// name it on a host where the format is not registered.
	FormatDMG = "dmg"
	// CompressionZstd is the default container compression.
	CompressionZstd = "zstd"

	// idHexLen is the name-segment reservation floor only. A segment
	// of this many or more hex digits is refused as an image name.
	// It is not the stored-id width; new ids are sumHexLen digits.
	// Do not decrease it; shortening would collide with names that
	// were previously valid hex segments.
	idHexLen  = 16
	sumHexLen = 64
)

// Payload kinds name the storage form of the active image payload.
const (
	// PayloadSingle is the default single-file image.sqsh payload.
	PayloadSingle = "single"
	// PayloadBlocks is a byte-block split payload. The full image
	// lives in _blocks/<full-id>/; each incremental layer with a
	// Blocks manifest lives in _blocks/<incr-id>/.
	PayloadBlocks = "blocks"
)

// MetaVersion is the legacy v1 metadata layout version. Version is
// a reserved compatibility field: a loader refuses every value it
// does not understand, so a future layout upgrade stays fail-closed.
// LoadManifest reads v1 documents only for isolated compatibility decoding.
const MetaVersion = "1.0"

// ManifestVersion is the DAG metadata layout version. LoadManifest
// also accepts MetaVersion and converts the linear v1 chain into a
// DAG; every other version stays refused. EncodeManifest writes
// only ManifestVersion, so rewriting a lazily migrated v1 document
// upgrades it.
const ManifestVersion = "2.0"

// Format is the container recorded on an image.
type Format struct {
	Type        string `yaml:"type"`
	Compression string `yaml:"compression"`
}

// chainEntryV1 is one incremental layer of a legacy v1 document.
// Kind is not repeated there; every member is incremental. Blocks
// is set when the layer is stored as _blocks/<id>/; digest and size
// still describe the whole logical squashfs.
type chainEntryV1 struct {
	ID        string       `yaml:"id"`
	Parent    string       `yaml:"parent"`
	Size      int64        `yaml:"size"`
	SHA256    string       `yaml:"sha256"`
	MtimeUnix int64        `yaml:"mtime_unix"`
	CreatedAt time.Time    `yaml:"created_at"`
	Blocks    []BlockEntry `yaml:"blocks,omitempty"`
}

// BlockEntry is one fixed-size byte block of a blocks payload. The
// blocks reassemble in manifest order into the single logical image
// file whose digest and size are recorded on the image node.
type BlockEntry struct {
	Name      string `yaml:"name"`
	Size      int64  `yaml:"size"`
	SHA256    string `yaml:"sha256"`
	MtimeUnix int64  `yaml:"mtime_unix"`
}

// metaV1 is the legacy v1 YAML document stored beside an image
// before the DAG layout. Only the compatibility decoder reads this
// shape; nothing writes it. Unknown fields are dropped on load.
type metaV1 struct {
	ID string `yaml:"id"`
	// Name is the canonical library name this image belongs to.
	Name string `yaml:"name"`
	// Version is the reserved metadata layout version. Legacy
	// documents carry MetaVersion, and documents predating the
	// reserved field carry an empty version that decodeV1
	// normalizes; validate refuses any other value.
	Version    string    `yaml:"version"`
	Kind       string    `yaml:"kind"`
	Format     Format    `yaml:"format"`
	Size       int64     `yaml:"size"`
	SHA256     string    `yaml:"sha256"`
	MtimeUnix  int64     `yaml:"mtime_unix"`
	CreatedAt  time.Time `yaml:"created_at"`
	Builder    string    `yaml:"builder"`
	Deprecated bool      `yaml:"deprecated"`
	// ContentSHA256 is the logical content fingerprint for a dmg
	// publish. Directory creates hash the packed source tree. Shadow
	// bakes hash the session upper.shadow after convert (hdiutil
	// rewrites that file, so the pre-convert bytes cannot mark an
	// idle bake). Empty on squashfs and erofs, where the packed
	// payload is already deterministic. An identical fingerprint is
	// an idle rebuild that skips hdiutil, because real disk images
	// embed timestamps.
	ContentSHA256 string         `yaml:"content_sha256,omitempty"`
	Chain         []chainEntryV1 `yaml:"chain,omitempty"`
	// Blocks is the byte-block manifest of a split payload. It stays
	// empty for the single-file form.
	Blocks []BlockEntry `yaml:"blocks,omitempty"`
}

// ImageMeta records one image node of the manifest DAG. The full
// root leaves Parent empty; an incremental node names the image it
// was built on, which is the full root or another incremental node
// when builds branch off a view that trails the head.
//
//nolint:revive // ImageMeta names one DAG node; the document type is Manifest.
type ImageMeta struct {
	ID     string `yaml:"id"`
	Kind   string `yaml:"kind"`
	Parent string `yaml:"parent,omitempty"`
	// Format and Description belong to individual nodes in the v3 layout.
	Format      Format `yaml:"format,omitempty"`
	Description string `yaml:"description,omitempty"`
	// Size and SHA256 describe the whole logical payload of this
	// node in both the single-file and the split form.
	Size      int64     `yaml:"size"`
	SHA256    string    `yaml:"sha256"`
	MtimeUnix int64     `yaml:"mtime_unix"`
	CreatedAt time.Time `yaml:"created_at"`
	Builder   string    `yaml:"builder"`
	// Deprecated is retained only in the v2 compatibility model.
	// ContentSHA256 is the logical content fingerprint of a dmg
	// publish; both stay facts of the full root.
	Deprecated    bool   `yaml:"deprecated,omitempty"`
	ContentSHA256 string `yaml:"content_sha256,omitempty"`
	// Blocks is the byte-block manifest of a split payload node. It
	// stays empty for the single-file form.
	Blocks []BlockEntry `yaml:"blocks,omitempty"`
}

// Manifest is the v2 metadata document stored beside an image
// repository: one full root plus the incremental nodes descending
// from it form a DAG, and Head names the active stack tip. The
// mount order of a tip is its root-to-tip path with the root first.
type Manifest struct {
	Version string `yaml:"version"`
	// Name is the canonical library name this manifest belongs to.
	Name   string `yaml:"name"`
	Format Format `yaml:"format"`
	// Head is the active stack tip. Every successful build moves it
	// to the new result, even when that result branches off the
	// previous head.
	Head string `yaml:"head"`
	// Images holds every node: exactly one full root and the
	// incremental nodes that descend from it.
	Images []ImageMeta `yaml:"images"`
}

// ID is the lowercase hex encoding of a sha256 sum. It equals the
// payload digest stored on meta.
func ID(sum [32]byte) string {
	return hex.EncodeToString(sum[:])
}

// PayloadKind reports the storage form of the manifest payload. The
// full root's blocks list decides for every node, because Validate
// keeps one payload kind across the DAG. A manifest without a full
// root reports the single-file form; resolution surfaces the graph
// error itself.
func (m Manifest) PayloadKind() string {
	root, err := m.Full()
	if err == nil && len(root.Blocks) > 0 {
		return PayloadBlocks
	}
	return PayloadSingle
}

// Full returns the unique full root of the DAG.
func (m Manifest) Full() (ImageMeta, error) {
	found := -1
	for i := range m.Images {
		if m.Images[i].Kind != KindFull {
			continue
		}
		if found >= 0 {
			return ImageMeta{}, errors.New("manifest: more than one full root")
		}
		found = i
	}
	if found < 0 {
		return ImageMeta{}, errors.New("manifest: no full root")
	}
	return m.Images[found], nil
}

// Node returns the image recorded under id.
func (m Manifest) Node(id string) (ImageMeta, bool) {
	for i := range m.Images {
		if m.Images[i].ID == id {
			return m.Images[i], true
		}
	}
	return ImageMeta{}, false
}

// Has reports whether id names a node of the DAG.
func (m Manifest) Has(id string) bool {
	_, ok := m.Node(id)
	return ok
}

// Stack returns the mount order from the full root to tip, root
// first. It fails when tip is unknown, its ancestors loop, or the
// ancestor path does not start at the full root.
func (m Manifest) Stack(tip string) ([]ImageMeta, error) {
	return walkStack(tip, m.Node)
}

func walkStack(tip string, lookup func(string) (ImageMeta, bool)) ([]ImageMeta, error) {
	var reversed []ImageMeta
	onPath := make(map[string]bool)
	for id := tip; id != ""; {
		img, ok := lookup(id)
		if !ok {
			return nil, fmt.Errorf("manifest: unknown image id %q", id)
		}
		if onPath[id] {
			return nil, fmt.Errorf("manifest: parent cycle at %q", id)
		}
		onPath[id] = true
		reversed = append(reversed, img)
		id = img.Parent
	}
	if len(reversed) == 0 {
		return nil, errors.New("manifest: empty stack tip")
	}
	stack := make([]ImageMeta, len(reversed))
	for i, img := range reversed {
		stack[len(reversed)-1-i] = img
	}
	if stack[0].Kind != KindFull {
		return nil, fmt.Errorf(
			"manifest: stack of %q does not start at the full root", tip)
	}
	return stack, nil
}

// HeadStack returns the active stack: the root-to-head path.
func (m Manifest) HeadStack() ([]ImageMeta, error) {
	return m.Stack(m.Head)
}

// layerNodes returns every incremental node of the DAG: the union of
// the reachability sets of all tips. These are the nodes whose layer
// files (_incr/<id>.sqsh or _blocks/<id>/) travel between the active
// active-slot layouts; the full root's payload travels with the slot
// layout itself, so sweeping and reclaim callers never relocate it
// as a layer.
func (m Manifest) layerNodes() []ImageMeta {
	nodes := make([]ImageMeta, 0, len(m.Images))
	for _, img := range m.Images {
		if img.Kind == KindIncremental {
			nodes = append(nodes, img)
		}
	}
	return nodes
}

// Reachable returns the id set holding every tip together with all
// of its ancestors. Unknown tips contribute nothing.
func (m Manifest) Reachable(tips ...string) map[string]bool {
	seen := make(map[string]bool)
	for _, tip := range tips {
		for id := tip; id != "" && !seen[id]; {
			img, ok := m.Node(id)
			if !ok {
				break
			}
			seen[id] = true
			id = img.Parent
		}
	}
	return seen
}

// Branches counts the DAG nodes off the head stack: layers built on
// a view that trailed the head, which later head moves do not
// reach. A linear DAG reports zero.
func (m Manifest) Branches() int {
	return len(m.Images) - len(m.Reachable(m.Head))
}

// manifestFromV1 converts a legacy v1 document into the DAG form.
// The v1 root is the full image and every chain member is
// incremental, so the recorded parent edges form a linear DAG; head
// becomes the chain tail, or the full id when the chain is empty.
func manifestFromV1(meta metaV1) Manifest {
	manifest := Manifest{
		Version: ManifestVersion,
		Name:    meta.Name,
		Format:  meta.Format,
		Head:    meta.ID,
		Images:  make([]ImageMeta, 0, 1+len(meta.Chain)),
	}
	manifest.Images = append(manifest.Images, ImageMeta{
		ID:            meta.ID,
		Kind:          KindFull,
		Size:          meta.Size,
		SHA256:        meta.SHA256,
		MtimeUnix:     meta.MtimeUnix,
		CreatedAt:     meta.CreatedAt,
		Builder:       meta.Builder,
		Deprecated:    meta.Deprecated,
		ContentSHA256: meta.ContentSHA256,
		Blocks:        meta.Blocks,
	})
	for _, entry := range meta.Chain {
		manifest.Images = append(manifest.Images, ImageMeta{
			ID:        entry.ID,
			Kind:      KindIncremental,
			Parent:    entry.Parent,
			Size:      entry.Size,
			SHA256:    entry.SHA256,
			MtimeUnix: entry.MtimeUnix,
			CreatedAt: entry.CreatedAt,
			Blocks:    entry.Blocks,
		})
		manifest.Head = entry.ID
	}
	return manifest
}
