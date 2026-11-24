package checkpoint

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"
)

// MarkerName identifies a directory as a checkpoint.
const MarkerName = ".tiler-checkpoint"

const markerVersion = 1

// Marker binds a checkpoint to its resolved target and creation time.
// It is not an authentication mechanism.
type Marker struct {
	Version   int       `json:"version"`
	Target    string    `json:"target"`
	CreatedAt time.Time `json:"created_at"`
}

func (m Marker) validate(target string) error {
	if m.Version != markerVersion {
		return fmt.Errorf("unsupported checkpoint marker version %d", m.Version)
	}
	if !filepath.IsAbs(m.Target) || filepath.Clean(m.Target) != m.Target || m.Target == string(filepath.Separator) {
		return errors.New("invalid checkpoint marker target")
	}
	if m.Target != target {
		return errors.New("checkpoint marker target mismatch")
	}
	if m.CreatedAt.IsZero() || m.CreatedAt.Year() < 1 || m.CreatedAt.Year() > 9999 {
		return errors.New("invalid checkpoint marker creation time")
	}
	return nil
}

// ParseMarker accepts exactly one marker and verifies its target binding.
func ParseMarker(data []byte, target string) (Marker, error) {
	var m Marker
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return Marker{}, fmt.Errorf("decode checkpoint marker: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return Marker{}, errors.New("checkpoint marker has trailing data")
	}
	if err := m.validate(target); err != nil {
		return Marker{}, err
	}
	return m, nil
}
