package image

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const importTxnJournal = ".import-txn.json"

// importStageRel and importBackupRel are repo-relative directories.
// Recovery deletes only paths the journal names under them.
const (
	importStageRel  = ".import-stage"
	importBackupRel = ".import-backup"
)

// ErrImportCleanupPending means the import journal is still durable.
// A later recovery retries it. The error does not authorize a broader delete.
var ErrImportCleanupPending = errors.New("import: cleanup pending")

// ImportPhase is the durable commit mark. Identical before and after
// manifests are a pure conversion; phase, not YAML equality, decides it.
type ImportPhase string

const (
	// ImportPrepared restores the previous manifest and promotion backups.
	ImportPrepared ImportPhase = "prepared"
	// ImportCommitted keeps published payloads and drops owned leftovers.
	ImportCommitted ImportPhase = "committed"
)

// ImportBackup is one replaced target leaf and its transaction-owned copy.
// Live and Saved are slash-separated paths relative to the target repository.
type ImportBackup struct {
	Live  string `json:"live"`
	Saved string `json:"saved"`
}

// ImportTxn is the single target-library record for one import.
// Staged paths sit under .import-stage. Objects are new version ids
// absent from Before. Backups cover promotions of existing leaves.
type ImportTxn struct {
	Name    string          `json:"name"`
	Phase   ImportPhase     `json:"phase"`
	Before  VersionManifest `json:"before"`
	After   VersionManifest `json:"after"`
	Staged  []string        `json:"staged,omitempty"`
	Objects []string        `json:"objects,omitempty"`
	Backups []ImportBackup  `json:"backups,omitempty"`
}

// SaveImportTxn durably replaces the library's import journal.
// It does not publish metadata or payload files.
func SaveImportTxn(imagesDir string, txn ImportTxn) error {
	repo, err := OpenRepo(imagesDir, txn.Name)
	if err != nil {
		return err
	}
	if err := validImportTxn(repo, txn); err != nil {
		return err
	}
	if err := importRemovalPending(imagesDir); err != nil {
		return err
	}
	body, err := json.Marshal(txn)
	if err != nil {
		return err
	}
	tmp, err := writeTemp(imagesDir, importTxnJournal, body, hooks{})
	if err != nil {
		return err
	}
	defer removeFile(tmp)
	return (hooks{}).do(tmp, filepath.Join(imagesDir, importTxnJournal))
}

func importRemovalPending(imagesDir string) error {
	_, err := os.Lstat(filepath.Join(imagesDir, versionRemovalJournal))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.New("import: version removal recovery is pending")
}

func loadImportTxn(imagesDir string) (ImportTxn, bool, error) {
	body, err := os.ReadFile(filepath.Join(imagesDir, importTxnJournal))
	if errors.Is(err, os.ErrNotExist) {
		return ImportTxn{}, false, nil
	}
	if err != nil {
		return ImportTxn{}, false, err
	}
	if !json.Valid(body) {
		return ImportTxn{}, false, errors.New("import: invalid transaction")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var txn ImportTxn
	if err := dec.Decode(&txn); err != nil {
		return ImportTxn{}, false, fmt.Errorf("import: read transaction: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return ImportTxn{}, false, errors.New("import: invalid transaction")
	}
	return txn, true, nil
}
