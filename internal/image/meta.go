package image

import (
	"encoding/hex"
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
}

// ID is the first 16 lowercase hex digits of a sha256 sum.
func ID(sum [32]byte) string {
	return hex.EncodeToString(sum[:idHexLen/2])
}
