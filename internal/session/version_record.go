package session

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/jiangxin/tiler/internal/image"
)

// VersionRecordVersion identifies records with complete immutable version pins.
const VersionRecordVersion = "1.0"

// NewVersionRecord creates a fresh identity even when a session name is reused.
func NewVersionRecord(name string, now time.Time) (Record, error) {
	if err := ValidateName(name); err != nil {
		return Record{}, err
	}
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return Record{}, err
	}
	rec := Record{Version: VersionRecordVersion, InstanceID: hex.EncodeToString(nonce[:]),
		Name: name, State: StateActive, CreatedAt: now.UTC()}
	return rec, rec.ValidateVersion()
}

// ValidateVersion refuses legacy records and incomplete desired or served pins.
func (r Record) ValidateVersion() error {
	if err := r.Validate(); err != nil {
		return err
	}
	if r.Version != VersionRecordVersion {
		return fmt.Errorf("session: unsupported record version %q; rebuild sessions", r.Version)
	}
	if !completeHash(r.InstanceID) || r.CreatedAt.IsZero() {
		return fmt.Errorf("session: %s: invalid instance identity or creation time", r.Name)
	}
	for _, action := range r.Actions {
		if action.Type == TypeMount {
			if err := action.validateVersionIdentity(); err != nil {
				return fmt.Errorf("session: %s: action %d: %w", r.Name, action.Seq, err)
			}
		}
	}
	return nil
}

func (a Action) validateVersionIdentity() error {
	if !completeHash(a.TipID) || a.RuntimeID != runtimeIDOf(a.Seq) {
		return fmt.Errorf("complete tip hash and stable runtime identity are required")
	}
	if a.Name == "" {
		if !filepath.IsAbs(a.Image) || a.FullID != "" || a.ServedID != "" || a.ServedTip != "" {
			return fmt.Errorf("file mounts require an absolute path and only a complete content tip hash")
		}
		return nil
	}
	parts, err := image.ParseName(a.Name)
	if err != nil || strings.Join(parts, "/") != a.Name || !completeHash(a.FullID) {
		return fmt.Errorf("canonical image name and complete full hash are required")
	}
	if (a.ServedID != "" || a.ServedTip != "") && (!completeHash(a.ServedID) || !completeHash(a.ServedTip)) {
		return fmt.Errorf("served full and tip hashes must be paired and complete")
	}
	return nil
}

func completeHash(value string) bool {
	_, err := hex.DecodeString(value)
	return len(value) == 64 && value == strings.ToLower(value) && err == nil
}

// SaveVersion validates the new schema before using the existing atomic store.
// Callers must hold the same locks required by Save.
func (s Store) SaveVersion(rec Record) error {
	if s.scope != nil {
		return s.Save(rec)
	}
	if err := rec.ValidateVersion(); err != nil {
		return err
	}
	return s.Save(rec)
}

// LoadVersion rejects old records without rewriting or upgrading their contents.
func (s Store) LoadVersion(name string) (Record, error) {
	rec, err := s.Load(name)
	if err == nil {
		if err = rec.ValidateVersion(); err != nil {
			err = fmt.Errorf("session: parse %s: %w", filepath.Join(s.Dir, name, recordFileName), err)
		}
	}
	return rec, err
}
