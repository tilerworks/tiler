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
	ImagesDir             *string            `yaml:"images_dir"`
	SessionsDir           *string            `yaml:"sessions_dir"`
	CacheDir              *string            `yaml:"cache_dir"`
	KeepVersions          *keepVersionsValue `yaml:"keep_versions"`
	MaxAge                *maxAgeText        `yaml:"max_age"`
	SessionMaxAge         *sessionMaxAgeText `yaml:"session_max_age"`
	SessionAutocloseStale *bool              `yaml:"session_autoclose_stale"`
	SquashfsOptions       *string            `yaml:"squashfs_options"`
	FuseOptions           *string            `yaml:"fuse_options"`
	SplitEnabled          *bool              `yaml:"split_enabled"`
	SplitMode             *string            `yaml:"split_mode"`
	SplitRoute            *string            `yaml:"split_route"`
	CacheEnabled          *bool              `yaml:"cache_enabled"`
	LogLevel              *string            `yaml:"log_level"`
}

// keepVersionsValue stores a base-10 integer scalar. A float is rejected
// instead of truncated, and zero stays present.
type keepVersionsValue struct {
	n int
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
		ImagesDir:             raw.ImagesDir,
		SessionsDir:           raw.SessionsDir,
		CacheDir:              raw.CacheDir,
		KeepVersions:          keepVersionsPtr(raw.KeepVersions),
		MaxAge:                maxAge,
		SessionMaxAge:         sessionMaxAge,
		SessionAutocloseStale: raw.SessionAutocloseStale,
		SquashfsOptions:       raw.SquashfsOptions,
		FuseOptions:           raw.FuseOptions,
		SplitEnabled:          raw.SplitEnabled,
		SplitMode:             raw.SplitMode,
		SplitRoute:            raw.SplitRoute,
		CacheEnabled:          raw.CacheEnabled,
		LogLevel:              raw.LogLevel,
	}, nil
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
