package image

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

const (
	// KindFull is a complete squashfs image.
	KindFull = "full"
	// KindIncremental is one upper-layer squashfs.
	KindIncremental = "incremental"

	// FormatSquashfs is the only image container.
	FormatSquashfs = "squashfs"
	// CompressionZstd is the only squashfs compression.
	CompressionZstd = "zstd"

	idHexLen  = 16
	sumHexLen = 64
)

// Payload kinds name the storage form of the active image payload.
const (
	// PayloadSingle is the default single-file image.sqsh payload.
	PayloadSingle = "single"
	// PayloadBlocks is a byte-block split payload stored in the
	// blocks/<full-id> directory.
	PayloadBlocks = "blocks"
	// PayloadMembers is a directory-member split payload stored in
	// the members/ directory.
	PayloadMembers = "members"
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
// every member is incremental.
type ChainEntry struct {
	ID        string    `yaml:"id"`
	Parent    string    `yaml:"parent"`
	Size      int64     `yaml:"size"`
	SHA256    string    `yaml:"sha256"`
	MtimeUnix int64     `yaml:"mtime_unix"`
	Source    Source    `yaml:"source"`
	CreatedAt time.Time `yaml:"created_at"`
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

// MemberEntry is one top-level directory partition of a members
// payload, packed as its own squashfs file at members/<name>.sqsh.
// These split members are side-by-side directory partitions and are
// distinct from the cascade stack layers that Target and mount
// requests also call members.
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
	ID         string       `yaml:"id"`
	Owner      string       `yaml:"owner"`
	Repo       string       `yaml:"repo"`
	Version    string       `yaml:"version"`
	Kind       string       `yaml:"kind"`
	Format     Format       `yaml:"format"`
	Size       int64        `yaml:"size"`
	SHA256     string       `yaml:"sha256"`
	MtimeUnix  int64        `yaml:"mtime_unix"`
	Source     Source       `yaml:"source"`
	CreatedAt  time.Time    `yaml:"created_at"`
	Builder    string       `yaml:"builder"`
	Deprecated bool         `yaml:"deprecated"`
	Chain      []ChainEntry `yaml:"chain,omitempty"`
	// Blocks is the byte-block manifest of a split payload. It stays
	// empty for the single-file and members forms.
	Blocks []BlockEntry `yaml:"blocks,omitempty"`
	// Members is the directory-partition manifest of a split
	// payload. It stays empty for the single-file and blocks forms.
	Members []MemberEntry `yaml:"members,omitempty"`
}

// ID is the first 16 lowercase hex digits of a sha256 sum.
func ID(sum [32]byte) string {
	return hex.EncodeToString(sum[:idHexLen/2])
}

// PayloadKind reports the storage form of the active payload. Load,
// publish, and resolve paths dispatch on it. The split forms are
// mutually exclusive once Validate passes, so any dispatch order is
// unambiguous.
func PayloadKind(m Meta) string {
	switch {
	case len(m.Blocks) > 0:
		return PayloadBlocks
	case len(m.Members) > 0:
		return PayloadMembers
	default:
		return PayloadSingle
	}
}

// membersDigest derives the logical whole-image digest of a members
// payload: sha256 over one "name size sha256" line per member, in
// manifest order. A members payload has no single overall file, so
// Meta.SHA256 records this digest and Meta.Size the sum of member
// sizes.
func membersDigest(members []MemberEntry) string {
	var text []byte
	for _, entry := range members {
		text = append(text, fmt.Sprintf("%s %d %s\n",
			entry.Name, entry.Size, entry.SHA256)...)
	}
	return hexDigest(sha256.Sum256(text))
}
