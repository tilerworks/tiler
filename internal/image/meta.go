package image

import (
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
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

// Format is the container recorded on an image.
type Format struct {
	Type        string `yaml:"type"`
	Compression string `yaml:"compression"`
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
	// placeAfter asks publication to keep this local build later than
	// every version already stored. It is not written to metadata.
	placeAfter bool
}

// Manifest is the in-memory single-root graph used by older build
// steps. meta.yaml stores a VersionManifest instead. Head names the
// active stack tip. The mount order of a tip is its root-to-tip
// path with the root first.
type Manifest struct {
	Version int
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

// versionDocument is the meta.yaml form of an in-memory graph.
// Node formats fall back to the shared manifest format.
func (m Manifest) versionDocument() VersionManifest {
	images := slices.Clone(m.Images)
	for i := range images {
		if images[i].Format == (Format{}) {
			images[i].Format = m.Format
		}
	}
	return VersionManifest{Version: VersionManifestVersion, Name: m.Name, Images: images}
}

// manifestFromVersion adapts a stored version forest to the
// in-memory single-root graph. Head is the latest version.
func manifestFromVersion(doc VersionManifest) Manifest {
	var format Format
	for _, node := range doc.Images {
		if node.Kind == KindFull && node.Parent == "" {
			format = node.Format
			break
		}
	}
	head := ""
	if forest, err := NewForest(doc.Images); err == nil {
		if latest, ok := forest.Latest(); ok {
			head = latest.ID
		}
	}
	return Manifest{Version: doc.Version, Name: doc.Name, Format: format, Head: head, Images: doc.Images}
}
