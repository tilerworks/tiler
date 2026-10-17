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
)

const (
	// DefaultLogLevel is used when no document sets log_level.
	DefaultLogLevel = "info"
	// DefaultSessionMaxAge is thirty days. Callers must keep an
	// explicit zero distinct from this default.
	DefaultSessionMaxAge = 720 * time.Hour

	// SplitModeBlocks stores a split image as content blocks.
	SplitModeBlocks = "blocks"
	// SplitModeMembers stores a split image as member files.
	SplitModeMembers = "members"

	// SplitRouteReassemble materializes the byte blocks into one
	// cache file before mounting. It is the default route.
	SplitRouteReassemble = "reassemble"
	// SplitRouteConcat mounts a concat FUSE view of the block
	// directory without any reassembly.
	SplitRouteConcat = "concat"
)

// Config is the resolved settings after defaults and overlays.
type Config struct {
	// ImagesDir stores read-only squashfs images.
	ImagesDir string
	// RuntimeDir holds per-mount runtime directories.
	RuntimeDir string
	// SessionsDir holds session records.
	SessionsDir string
	// CacheDir is the directory a later step copies images into when
	// NAS cache is enabled.
	CacheDir string
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
	// SquashfsOptions is an opaque option string for a later mount.
	SquashfsOptions string
	// FuseOptions is an opaque FUSE option string for a later mount.
	FuseOptions string
	// SplitEnabled selects split image storage.
	SplitEnabled bool
	// SplitMode is blocks or members when split is enabled, and empty
	// when split is off.
	SplitMode string
	// SplitRoute selects the byte-block mount route: reassemble or
	// concat. Empty behaves as reassemble. A route is meaningful only
	// with split enabled in blocks mode.
	SplitRoute string
	// CacheEnabled selects a later copy into CacheDir.
	CacheEnabled bool
	// LogLevel is debug, info, warn, or error.
	LogLevel string
	// reservedRoot is the cleaned $HOME/.tiler tree. It stays reserved
	// even when the four directories are moved elsewhere. Load and
	// Defaults set it; Apply keeps the value from the lower config.
	reservedRoot string
}

// Patch records fields present in one document. Nil means omitted.
type Patch struct {
	ImagesDir             *string
	RuntimeDir            *string
	SessionsDir           *string
	CacheDir              *string
	KeepVersions          *int
	MaxAge                *time.Duration
	SessionMaxAge         *time.Duration
	SessionAutocloseStale *bool
	SquashfsOptions       *string
	FuseOptions           *string
	SplitEnabled          *bool
	SplitMode             *string
	SplitRoute            *string
	CacheEnabled          *bool
	LogLevel              *string
}

// Defaults returns the settings used when no document sets a field.
// home is the injected user home directory, not the process environment.
func Defaults(home string) Config {
	root := filepath.Join(home, ".tiler")
	return Config{
		ImagesDir:     filepath.Join(root, "images"),
		RuntimeDir:    filepath.Join(root, "runtime"),
		SessionsDir:   filepath.Join(root, "sessions"),
		CacheDir:      filepath.Join(root, "cache"),
		SessionMaxAge: DefaultSessionMaxAge,
		LogLevel:      DefaultLogLevel,
		reservedRoot:  root,
	}
}

// Apply copies each present patch field onto base. Omitted fields stay
// as they are on base.
func (p Patch) Apply(base Config) Config {
	base.ImagesDir = overlay(base.ImagesDir, p.ImagesDir)
	base.RuntimeDir = overlay(base.RuntimeDir, p.RuntimeDir)
	base.SessionsDir = overlay(base.SessionsDir, p.SessionsDir)
	base.CacheDir = overlay(base.CacheDir, p.CacheDir)
	base.KeepVersions = overlay(base.KeepVersions, p.KeepVersions)
	base.MaxAge = overlay(base.MaxAge, p.MaxAge)
	base.SessionMaxAge = overlay(base.SessionMaxAge, p.SessionMaxAge)
	base.SessionAutocloseStale = overlay(base.SessionAutocloseStale, p.SessionAutocloseStale)
	base.SquashfsOptions = overlay(base.SquashfsOptions, p.SquashfsOptions)
	base.FuseOptions = overlay(base.FuseOptions, p.FuseOptions)
	base.SplitEnabled = overlay(base.SplitEnabled, p.SplitEnabled)
	base.SplitMode = overlay(base.SplitMode, p.SplitMode)
	base.SplitRoute = overlay(base.SplitRoute, p.SplitRoute)
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
	return validateSplit(cfg.SplitEnabled, cfg.SplitMode, cfg.SplitRoute)
}

func validateLogLevel(level string) error {
	switch level {
	case "debug", "info", "warn", "error":
		return nil
	default:
		return fmt.Errorf("log_level: unknown value %q", level)
	}
}

func validateSplit(enabled bool, mode, route string) error {
	switch mode {
	case "", SplitModeBlocks, SplitModeMembers:
	default:
		return fmt.Errorf("split_mode: unknown value %q", mode)
	}
	if enabled && mode == "" {
		return errors.New("split_enabled: requires split_mode blocks or members")
	}
	if !enabled && mode != "" {
		return errors.New("split_mode: must be empty when split_enabled is false")
	}
	return validateSplitRoute(enabled, mode, route)
}

// validateSplitRoute accepts the empty default plus the two known
// routes. A chosen route pins the byte-block mount form, so it is
// admitted only for an enabled blocks split.
func validateSplitRoute(enabled bool, mode, route string) error {
	switch route {
	case "", SplitRouteReassemble, SplitRouteConcat:
	default:
		return fmt.Errorf("split_route: unknown value %q", route)
	}
	if route != "" && (!enabled || mode != SplitModeBlocks) {
		return errors.New("split_route: requires split_enabled with split_mode blocks")
	}
	return nil
}
