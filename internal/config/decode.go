package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

// rawPatch is the YAML shape. Pointers stay nil when the key is absent.
// A present zero or false is non-nil so it can overlay a lower value.
type rawPatch struct {
	Version               *versionValue      `yaml:"version"`
	ImagesDir             *string            `yaml:"images_dir"`
	SessionsDir           *string            `yaml:"sessions_dir"`
	CacheDir              *string            `yaml:"cache_dir"`
	KeepVersions          *keepVersionsValue `yaml:"keep_versions"`
	MaxAge                *maxAgeText        `yaml:"max_age"`
	SessionMaxAge         *sessionMaxAgeText `yaml:"session_max_age"`
	SessionAutocloseStale *bool              `yaml:"session_autoclose_stale"`
	SquashfsOptions       *string            `yaml:"squashfs_options"`
	FuseOptions           *string            `yaml:"fuse_options"`
	SplitSize             *splitSizeText     `yaml:"split_size"`
	CacheEnabled          *bool              `yaml:"cache_enabled"`
	LogLevel              *string            `yaml:"log_level"`
}

// versionValue stores a base-10 integer schema version. A float is
// rejected instead of truncated, and zero stays present.
type versionValue struct {
	n int
}

func (v *versionValue) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.ScalarNode {
		return errors.New("version: invalid integer")
	}
	n, err := strconv.ParseInt(value.Value, 10, 0)
	if err != nil {
		return fmt.Errorf("version: %w", err)
	}
	v.n = int(n)
	return nil
}

// keepVersionsValue stores a base-10 integer scalar. A float is rejected
// instead of truncated, and zero stays present.
type keepVersionsValue struct {
	n int
}

// splitSizeText stores scalar text so 0 and 2G both count as present.
// The value is an integer byte count or a ParseByteSize string.
type splitSizeText struct {
	n int64
}

func (s *splitSizeText) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.ScalarNode {
		return errors.New("split_size: invalid size")
	}
	n, err := ParseByteSize(value.Value)
	if err != nil {
		return fmt.Errorf("split_size: %w", err)
	}
	s.n = n
	return nil
}

func (k *keepVersionsValue) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.ScalarNode {
		return errors.New("keep_versions: invalid integer")
	}
	n, err := strconv.ParseInt(value.Value, 10, 0)
	if err != nil {
		return fmt.Errorf("keep_versions: %w", err)
	}
	k.n = int(n)
	return nil
}

// durationText stores scalar text so 0 and 0s both count as present.
type durationText struct {
	text string
}

// maxAgeText names max_age when the node is not a scalar.
type maxAgeText struct {
	durationText
}

func (d *maxAgeText) UnmarshalYAML(value *yaml.Node) error {
	return unmarshalDuration("max_age", &d.durationText, value)
}

// sessionMaxAgeText names session_max_age when the node is not a scalar.
type sessionMaxAgeText struct {
	durationText
}

func (d *sessionMaxAgeText) UnmarshalYAML(value *yaml.Node) error {
	return unmarshalDuration("session_max_age", &d.durationText, value)
}

func unmarshalDuration(field string, dest *durationText, value *yaml.Node) error {
	if value.Kind != yaml.ScalarNode {
		return fmt.Errorf("%s: invalid duration", field)
	}
	dest.text = value.Value
	return nil
}

// Decode reads one YAML document into a patch. Unknown keys fail.
// An empty document yields an empty patch. Duration fields are parsed
// here; combination checks belong to Validate after Apply.
func Decode(data []byte) (Patch, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var raw rawPatch
	if err := dec.Decode(&raw); err != nil {
		if errors.Is(err, io.EOF) {
			return Patch{}, nil
		}
		return Patch{}, err
	}
	return raw.patch()
}

func (raw rawPatch) patch() (Patch, error) {
	maxAge, err := durationField("max_age", raw.MaxAge.duration())
	if err != nil {
		return Patch{}, err
	}
	sessionMaxAge, err := durationField("session_max_age", raw.SessionMaxAge.duration())
	if err != nil {
		return Patch{}, err
	}
	return Patch{
		Version:               versionPtr(raw.Version),
		ImagesDir:             raw.ImagesDir,
		SessionsDir:           raw.SessionsDir,
		CacheDir:              raw.CacheDir,
		KeepVersions:          keepVersionsPtr(raw.KeepVersions),
		MaxAge:                maxAge,
		SessionMaxAge:         sessionMaxAge,
		SessionAutocloseStale: raw.SessionAutocloseStale,
		SquashfsOptions:       raw.SquashfsOptions,
		FuseOptions:           raw.FuseOptions,
		SplitSize:             splitSizePtr(raw.SplitSize),
		CacheEnabled:          raw.CacheEnabled,
		LogLevel:              raw.LogLevel,
	}, nil
}

func versionPtr(v *versionValue) *int {
	if v == nil {
		return nil
	}
	n := v.n
	return &n
}

func splitSizePtr(v *splitSizeText) *int64 {
	if v == nil {
		return nil
	}
	n := v.n
	return &n
}

func keepVersionsPtr(v *keepVersionsValue) *int {
	if v == nil {
		return nil
	}
	n := v.n
	return &n
}

func (d *maxAgeText) duration() *durationText {
	if d == nil {
		return nil
	}
	return &d.durationText
}

func (d *sessionMaxAgeText) duration() *durationText {
	if d == nil {
		return nil
	}
	return &d.durationText
}

func durationField(name string, text *durationText) (*time.Duration, error) {
	if text == nil {
		return nil, nil
	}
	d, err := ParseDuration(text.text)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return &d, nil
}
