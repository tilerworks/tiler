// Package config holds resolved tiler settings and the patch used to
// overlay one document onto another. Nil patch fields are omitted and
// keep the lower value. A non-nil field is explicit, so zero and false
// still replace the lower value. An explicit session max age of zero
// disables age cleanup and is not the 720h default.
package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/jiangxin/tiler/internal/container"
)

const (
	// CurrentConfigVersion is the schema version written into
	// .tiler.yaml. Missing or older versions trigger an automatic
	// user-file upgrade before Load's strict Decode.
	CurrentConfigVersion = 1

	// DefaultLogLevel is used when no document sets log_level.
	DefaultLogLevel = "info"
	// DefaultSessionMaxAge is thirty days. Callers must keep an
	// explicit zero distinct from this default.
	DefaultSessionMaxAge = 720 * time.Hour
)

// Config is the resolved settings after defaults and overlays.
type Config struct {
	// Version is the .tiler.yaml schema version. Defaults to
	// CurrentConfigVersion when no document sets it.
	Version int
	// ImagesDir stores read-only container images.
	ImagesDir string
	// SessionsDir is the merged root for session records and
	// per-session runtime combinations.
	SessionsDir string
	// CacheDir is the directory a later step copies images into when
	// NAS cache is enabled. Blocks builds also stage pack scratch here.
	CacheDir string
	// LogsDir holds session and image-version activity logs.
	LogsDir string
	// KeepVersions is how many image versions to retain. Zero means
	// the count is unlimited.
	KeepVersions int
	// MaxAge is how long an image may be kept. Zero means age does
	// not limit images.
	MaxAge time.Duration
	// SessionMaxAge is how long a session may be kept. Zero disables
	// age cleanup.
	SessionMaxAge time.Duration
	// SessionAutocloseStale flips an aged active session without a
	// healthy mount to closed, which makes its record eligible for
	// age cleanup. Off by default: active records are never closed
	// automatically unless this is set.
	SessionAutocloseStale bool
	// SquashfsOptions is an opaque option string consumed only by the
	// squashfs mount implementation. Erofs has no separate options key.
	SquashfsOptions string
	// ImageFormat is the container type used when a build omits
	// --format. Empty leaves the choice to the installed toolchains.
	ImageFormat string
	// ImageCompression is the compression used when a build omits
	// --compression. Empty selects the format default.
	ImageCompression string
	// FuseOptions is an opaque FUSE option string for a later mount.
	FuseOptions string
	// SplitSize is the block size in bytes. Zero disables splitting.
	// An explicit zero is distinct from an omitted key only on Patch;
	// both resolve to the same off state here.
	SplitSize int64
	// CacheEnabled selects a later copy into CacheDir.
	CacheEnabled bool
	// LogLevel is debug, info, warn, or error.
	LogLevel string
	// reservedRoot is the cleaned $HOME/.tiler tree. It stays reserved
	// even when the configured directories are moved elsewhere. Load
	// and Defaults set it; Apply keeps the value from the lower config.
	reservedRoot string
	// userFile is the $HOME/.tiler.yaml path. Defaults sets it and
	// Apply keeps the value from the lower config, so it survives
	// project and command-line overlays.
	userFile string
	// configFile is the nearest .tiler.yaml Load actually read. It
	// starts as the user file path and becomes a project file when
	// the upward walk finds one.
	configFile string
}

// Patch records fields present in one document. Nil means omitted.
type Patch struct {
	Version               *int
	ImagesDir             *string
	SessionsDir           *string
	CacheDir              *string
	LogsDir               *string
	KeepVersions          *int
	MaxAge                *time.Duration
	SessionMaxAge         *time.Duration
	SessionAutocloseStale *bool
	SquashfsOptions       *string
	FuseOptions           *string
	ImageFormat           *string
	ImageCompression      *string
	SplitSize             *int64
	CacheEnabled          *bool
	LogLevel              *string
}

// Defaults returns the settings used when no document sets a field.
// home is the injected user home directory, not the process environment.
func Defaults(home string) Config {
	root := filepath.Join(home, ".tiler")
	return Config{
		Version:       CurrentConfigVersion,
		ImagesDir:     filepath.Join(root, "images"),
		SessionsDir:   filepath.Join(root, "sessions"),
		CacheDir:      filepath.Join(root, "cache"),
		LogsDir:       filepath.Join(root, "logs"),
		SessionMaxAge: DefaultSessionMaxAge,
		LogLevel:      DefaultLogLevel,
		reservedRoot:  root,
		userFile:      filepath.Join(home, fileName),
		configFile:    filepath.Join(home, fileName),
	}
}

// UserFile returns the $HOME/.tiler.yaml path this config resolved
// from. It is the canonical user-level config location whether or not
// the file exists on disk.
func (c Config) UserFile() string {
	return c.userFile
}

// ConfigFile returns the nearest .tiler.yaml Load read. A project file
// found by walking upward from the working directory replaces the user
// file. When no file exists, the result stays the user file path.
func (c Config) ConfigFile() string {
	if c.configFile != "" {
		return c.configFile
	}
	return c.userFile
}

// Apply copies each present patch field onto base. Omitted fields stay
// as they are on base.
func (p Patch) Apply(base Config) Config {
	base.Version = overlay(base.Version, p.Version)
	base.ImagesDir = overlay(base.ImagesDir, p.ImagesDir)
	base.SessionsDir = overlay(base.SessionsDir, p.SessionsDir)
	base.CacheDir = overlay(base.CacheDir, p.CacheDir)
	base.LogsDir = overlay(base.LogsDir, p.LogsDir)
	base.KeepVersions = overlay(base.KeepVersions, p.KeepVersions)
	base.MaxAge = overlay(base.MaxAge, p.MaxAge)
	base.SessionMaxAge = overlay(base.SessionMaxAge, p.SessionMaxAge)
	base.SessionAutocloseStale = overlay(base.SessionAutocloseStale, p.SessionAutocloseStale)
	base.SquashfsOptions = overlay(base.SquashfsOptions, p.SquashfsOptions)
	base.FuseOptions = overlay(base.FuseOptions, p.FuseOptions)
	base.ImageFormat = overlay(base.ImageFormat, p.ImageFormat)
	base.ImageCompression = overlay(base.ImageCompression, p.ImageCompression)
	base.SplitSize = overlay(base.SplitSize, p.SplitSize)
	base.CacheEnabled = overlay(base.CacheEnabled, p.CacheEnabled)
	base.LogLevel = overlay(base.LogLevel, p.LogLevel)
	return base
}

func overlay[T any](base T, value *T) T {
	if value == nil {
		return base
	}
	return *value
}

// Validate checks a resolved configuration. The error names the field.
func Validate(cfg Config) error {
	if cfg.Version < 0 {
		return errors.New("version: must not be negative")
	}
	if err := validateLogLevel(cfg.LogLevel); err != nil {
		return err
	}
	if cfg.KeepVersions < 0 {
		return errors.New("keep_versions: must not be negative")
	}
	if cfg.MaxAge < 0 {
		return errors.New("max_age: must not be negative")
	}
	if cfg.SessionMaxAge < 0 {
		return errors.New("session_max_age: must not be negative")
	}
	if cfg.SplitSize < 0 {
		return errors.New("split_size: must not be negative")
	}
	return validateImageSettings(cfg)
}

// validateImageSettings checks an explicit container type and
// compression. An empty format is inferred later from the toolchain.
// A compression without a format must be legal for some registered
// format; paired with a format, it must be legal for that format.
func validateImageSettings(cfg Config) error {
	if cfg.ImageFormat == "" && cfg.ImageCompression == "" {
		return nil
	}
	if cfg.ImageFormat != "" {
		format, err := container.Lookup(cfg.ImageFormat)
		if err != nil {
			return fmt.Errorf("image_format: %w", err)
		}
		if cfg.ImageCompression != "" && !format.AllowsCompression(cfg.ImageCompression) {
			return fmt.Errorf("image_compression: %q is not allowed for %s",
				cfg.ImageCompression, format.Type())
		}
		return nil
	}
	if compressionAllowed(cfg.ImageCompression) {
		return nil
	}
	return fmt.Errorf("image_compression: %q is not allowed", cfg.ImageCompression)
}

func compressionAllowed(compression string) bool {
	for _, format := range container.All() {
		if format.AllowsCompression(compression) {
			return true
		}
	}
	return false
}

func validateLogLevel(level string) error {
	switch level {
	case "debug", "info", "warn", "error":
		return nil
	default:
		return fmt.Errorf("log_level: unknown value %q", level)
	}
}
