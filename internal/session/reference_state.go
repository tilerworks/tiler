package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ReferenceControlVersion identifies reference bookkeeping and journal schemas.
const ReferenceControlVersion = "1.0"

// ReferenceRevision describes one source record in the derived inventory.
type ReferenceRevision struct {
	InstanceID   string `json:"instance_id"`
	SHA256       string `json:"sha256"`
	Size         int64  `json:"size"`
	ModifiedNano int64  `json:"modified_nano"`
}

// ReferenceState is bookkeeping, not independent proof that an index is trusted.
type ReferenceState struct {
	Version    string                       `json:"version"`
	Generation uint64                       `json:"generation"`
	Dirty      bool                         `json:"dirty"`
	Inventory  map[string]ReferenceRevision `json:"inventory"`
}

// ReferenceJournal retains source snapshots until their generation is complete.
// A nil Before creates a session; a nil After removes its retained record.
type ReferenceJournal struct {
	Version    string  `json:"version"`
	Generation uint64  `json:"generation"`
	Before     *Record `json:"before"`
	After      *Record `json:"after"`
}

func (state ReferenceState) validate() error {
	if state.Version != ReferenceControlVersion || (state.Dirty && state.Generation == 0) {
		return errors.New("session: invalid reference state version or generation")
	}
	for name, revision := range state.Inventory {
		if ValidateName(name) != nil || !completeHash(revision.InstanceID) || !completeHash(revision.SHA256) ||
			revision.Size < 0 || revision.ModifiedNano <= 0 {
			return fmt.Errorf("session: invalid reference inventory entry %q", name)
		}
	}
	return nil
}

func (journal ReferenceJournal) validate() error {
	if journal.Version != ReferenceControlVersion || journal.Generation == 0 || (journal.Before == nil && journal.After == nil) {
		return errors.New("session: invalid reference journal version, generation or snapshots")
	}
	for _, rec := range []*Record{journal.Before, journal.After} {
		if rec != nil {
			if err := rec.ValidateVersion(); err != nil {
				return err
			}
		}
	}
	if journal.Before != nil && journal.After != nil && journal.Before.Name != journal.After.Name {
		return errors.New("session: reference journal changes session name")
	}
	return nil
}

// LoadReferenceState reads bookkeeping without deciding whether it is trusted.
func (s Store) LoadReferenceState() (state ReferenceState, err error) {
	err = s.loadReferenceControl("state.json", &state)
	return state, err
}

// LoadReferenceJournal returns os.ErrNotExist when no transaction is recorded.
func (s Store) LoadReferenceJournal() (journal ReferenceJournal, err error) {
	err = s.loadReferenceControl("journal.json", &journal)
	return journal, err
}

// SaveReferenceState requires the caller's library lock and transaction protocol.
func (s Store) SaveReferenceState(state ReferenceState) error {
	return s.saveReferenceControl("state.json", state)
}

// SaveReferenceJournal durably records snapshots under the same coordination lock.
func (s Store) SaveReferenceJournal(journal ReferenceJournal) error {
	return s.saveReferenceControl("journal.json", journal)
}

type referenceControl interface{ validate() error }

func (s Store) loadReferenceControl(name string, value referenceControl) error {
	path, err := s.referencePath(name)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err == nil {
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		err = decoder.Decode(value)
		if err == nil && !json.Valid(data) {
			err = errors.New("session: reference control must contain one JSON document")
		}
	}
	if err == nil {
		err = value.validate()
	}
	if err != nil {
		return fmt.Errorf("session: reference control %s: %w", path, err)
	}
	return nil
}

func (s Store) saveReferenceControl(name string, value referenceControl) error {
	if err := value.validate(); err != nil {
		return err
	}
	path, err := s.referencePath(name)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := writeAtomic(path, append(data, '\n')); err != nil {
		return err
	}
	return syncReferenceParents(filepath.Dir(path))
}

func syncReferenceParents(dir string) error {
	for {
		file, err := os.Open(dir)
		if err != nil {
			return err
		}
		if err := errors.Join(file.Sync(), file.Close()); err != nil {
			return err
		}
		if parent := filepath.Dir(dir); parent != dir {
			dir = parent
		} else {
			return nil
		}
	}
}
