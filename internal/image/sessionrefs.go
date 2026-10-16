package image

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// SessionRefs reports references from session records in one
// directory. Only *.yaml files that decode and carry an owner, a
// repo, a full id, and a state other than closed count; every other
// file is ignored. This scope never creates session records, and
// garbage collection never deletes them.
type SessionRefs struct {
	Dir string
}

// sessionRecord is the subset of a session document that reference
// protection needs. Later session files only have to carry these
// four fields to protect an image.
type sessionRecord struct {
	Owner  string `yaml:"owner"`
	Repo   string `yaml:"repo"`
	FullID string `yaml:"full_id"`
	State  string `yaml:"state"`
}

// sessionStateClosed marks a record that no longer pins an image.
const sessionStateClosed = "closed"

// Holds reports whether an open session record pins the image.
func (s SessionRefs) Holds(ref Ref) (bool, error) {
	entries, err := os.ReadDir(s.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		record, ok := loadSessionRecord(filepath.Join(s.Dir, entry.Name()))
		if !ok || record.State == sessionStateClosed {
			continue
		}
		if record.Owner == ref.Owner && record.Repo == ref.Repo &&
			record.FullID == ref.FullID {
			return true, nil
		}
	}
	return false, nil
}

// loadSessionRecord decodes one candidate file. A file that does not
// decode, or that decodes but misses one of the four fields, is
// ignored instead of failing the scan.
func loadSessionRecord(path string) (sessionRecord, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return sessionRecord{}, false
	}
	var record sessionRecord
	if err := yaml.Unmarshal(data, &record); err != nil {
		return sessionRecord{}, false
	}
	if record.Owner == "" || record.Repo == "" ||
		record.FullID == "" || record.State == "" {
		return sessionRecord{}, false
	}
	return record, true
}
