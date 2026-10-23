package image

import (
	"encoding/hex"
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

// Source is optional provenance. Empty fields are valid. A non-empty
// commit must be lowercase hexadecimal.
type Source struct {
	Repo   string `yaml:"repo"`
	Ref    string `yaml:"ref"`
	Commit string `yaml:"commit"`
}

// ChainEntry is one incremental layer. Kind is not repeated here;
// every member is incremental. Blocks is set when the layer is
// stored as _blocks/<id>/; digest and size still describe the whole
// logical squashfs.
type ChainEntry struct {
	ID        string       `yaml:"id"`
	Parent    string       `yaml:"parent"`
	Size      int64        `yaml:"size"`
	SHA256    string       `yaml:"sha256"`
	MtimeUnix int64        `yaml:"mtime_unix"`
	Source    Source       `yaml:"source"`
	CreatedAt time.Time    `yaml:"created_at"`
	Blocks    []BlockEntry `yaml:"blocks,omitempty"`
}

// BlockEntry is one fixed-size byte block of a blocks payload. The
// blocks reassemble in manifest order into the single logical image
// file whose digest and size are recorded on Meta.
type BlockEntry struct {
	Name      string `yaml:"name"`
	Size      int64  `yaml:"size"`
	SHA256    string `yaml:"sha256"`
	MtimeUnix int64  `yaml:"mtime_unix"`
}

// MemberEntry is retained only so historical members-form metadata
// still unmarshals; Validate rejects any non-empty Members list.
type MemberEntry struct {
	Name      string `yaml:"name"`
	Image     string `yaml:"image"`
	Size      int64  `yaml:"size"`
	SHA256    string `yaml:"sha256"`
	MtimeUnix int64  `yaml:"mtime_unix"`
}

// Meta is the YAML document stored beside an image. Unknown fields
// are dropped on load.
type Meta struct {
	ID         string    `yaml:"id"`
	Owner      string    `yaml:"owner"`
	Repo       string    `yaml:"repo"`
	Version    string    `yaml:"version"`
	Kind       string    `yaml:"kind"`
	Format     Format    `yaml:"format"`
	Size       int64     `yaml:"size"`
	SHA256     string    `yaml:"sha256"`
	MtimeUnix  int64     `yaml:"mtime_unix"`
	Source     Source    `yaml:"source"`
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
	ContentSHA256 string       `yaml:"content_sha256,omitempty"`
	Chain         []ChainEntry `yaml:"chain,omitempty"`
	// Blocks is the byte-block manifest of a split payload. It stays
	// empty for the single-file form.
	Blocks []BlockEntry `yaml:"blocks,omitempty"`
	// Members is the historical directory-partition manifest. Any
	// non-empty list is refused by Validate so old libraries fail
	// closed; the members form is no longer supported.
	Members []MemberEntry `yaml:"members,omitempty"`
}

// ID is the first 16 lowercase hex digits of a sha256 sum.
func ID(sum [32]byte) string {
	return hex.EncodeToString(sum[:idHexLen/2])
}

// PayloadKind reports the storage form of the active payload. Load,
// publish, and resolve paths dispatch on it. Members-form metadata
// never reaches here after Validate; an empty Blocks list is the
// single-file form.
func PayloadKind(m Meta) string {
	if len(m.Blocks) > 0 {
		return PayloadBlocks
	}
	return PayloadSingle
}
