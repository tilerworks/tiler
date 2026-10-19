// Package session owns the authoritative session records: ordered
// action logs that survive crashes and drive recovery, reverse
// lookup, and reference protection. A record is a JSON file under
// the configured sessions directory; an appended action is durable
// on disk before its mount executes. Mount and umount run through
// injected seams in later lifecycle code, so this package never
// hard-depends on the workspace engine.
package session

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

// Action types recorded in a session log.
const (
	// TypeMount is one mount action.
	TypeMount = "mount"
	// TypeUmount is one umount action.
	TypeUmount = "umount"
)

// Action statuses. A write-ahead append always lands as pending;
// the execution writes the final status back.
const (
	// StatusPending marks an action recorded before it executed.
	StatusPending = "pending"
	// StatusMounted marks a mount that succeeded.
	StatusMounted = "mounted"
	// StatusFailed marks an action whose execution failed.
	StatusFailed = "failed"
	// StatusUnmounted marks a mount that was torn down again.
	StatusUnmounted = "unmounted"
)

// Record states.
const (
	// StateActive marks a record that may still hold mounts.
	StateActive = "active"
	// StateClosed marks a finished record kept for audit.
	StateClosed = "closed"
)

// Mount modes recorded per action.
const (
	// ModeRO is a read-only view.
	ModeRO = "ro"
	// ModeRW is a writable merged view.
	ModeRW = "rw"
)

// Action is one recorded mount or umount. One record carries many
// mounts, so the image identity lives on the action, not the record.
// The JSON keys are the consumption contract: additive only.
type Action struct {
	// Seq is the 1-based position in the record's action order.
	Seq int `json:"seq"`
	// Type is mount or umount.
	Type string `json:"type"`
	// Image is the reference as given: an owner/repo library name or
	// the path of a single squashfs file.
	Image string `json:"image"`
	// Owner is the library owner, empty for a file-path image.
	Owner string `json:"owner,omitempty"`
	// Repo is the library repository, empty for a file-path image.
	Repo string `json:"repo,omitempty"`
	// FullID is the resolved full image id, empty for a file-path
	// image.
	FullID string `json:"full_id,omitempty"`
	// Mountpoint is the directory serving the view.
	Mountpoint string `json:"mountpoint"`
	// Mode is ro or rw for a mount; other action types leave it
	// empty.
	Mode string `json:"mode"`
	// Subdir is the image-relative mount root, empty for whole
	// images.
	Subdir string `json:"subdir,omitempty"`
	// Status is pending, mounted, failed, or unmounted.
	Status string `json:"status"`
	// Timestamp is when the action was recorded.
	Timestamp time.Time `json:"timestamp"`
}

// Record is one session: its state and the ordered action log that
// crash recovery replays and reverse lookup matches against.
type Record struct {
	// Name is the session name and the record file stem.
	Name string `json:"name"`
	// State is active or closed.
	State string `json:"state"`
	// Actions is the ordered log; mount order is record order.
	Actions []Action `json:"actions"`
	// CreatedAt is when the record was written first.
	CreatedAt time.Time `json:"created_at"`
	// ClosedAt is when the record reached the closed state; it stays
	// zero while active.
	ClosedAt time.Time `json:"closed_at"`
	// RemoveOnClose marks a --rm session: closing it deletes the
	// record and purges the runtime directory of every view, upper
	// data included, instead of keeping the record for audit. The
	// JSON key is additive and omitempty, so a record without it
	// round-trips unchanged.
	RemoveOnClose bool `json:"remove_on_close,omitempty"`
}

// ErrInvalidName is a session name that is not a safe single path
// segment.
var ErrInvalidName = errors.New("invalid session name")

// ValidateName accepts a non-empty single segment: no path
// separator, not "." or "..", and no whitespace or control
// characters. Session names never carry structure.
func ValidateName(name string) error {
	if name == "" || name == "." || name == ".." ||
		strings.ContainsAny(name, `/\`) || badNameRune(name) {
		return fmt.Errorf("%w: %q", ErrInvalidName, name)
	}
	return nil
}

// badNameRune reports a whitespace or control character.
func badNameRune(name string) bool {
	for _, r := range name {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return true
		}
	}
	return false
}

// implicitPrefix starts every implicitly created session name.
const implicitPrefix = "auto-"

// implicitRandomBytes is the entropy of the short random suffix.
const implicitRandomBytes = 4

// ImplicitName returns the implicit session name for one moment:
// auto-<compact UTC timestamp>-<short random hex>. Implicit sessions
// are isomorphic with explicit ones and share the same operations.
func ImplicitName(now time.Time) (string, error) {
	buf := make([]byte, implicitRandomBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("session: implicit name: %w", err)
	}
	stamp := now.UTC().Format("20060102T150405Z")
	return implicitPrefix + stamp + "-" + hex.EncodeToString(buf), nil
}

// implicitName reports whether a session name was generated for an
// implicit session. Only those auto-close with their last view.
func implicitName(name string) bool {
	return strings.HasPrefix(name, implicitPrefix)
}

// recordsMountpoint reports whether the record carries a mount
// action at the exact point, whatever its status.
func recordsMountpoint(rec Record, point string) bool {
	for _, action := range rec.Actions {
		if action.Type == TypeMount && action.Mountpoint == point {
			return true
		}
	}
	return false
}

// Validate checks the record shape: a legal name, a known state, and
// actions with known enums and unique positive sequence numbers.
func (r Record) Validate() error {
	if err := ValidateName(r.Name); err != nil {
		return fmt.Errorf("session: %w", err)
	}
	switch r.State {
	case StateActive, StateClosed:
	default:
		return fmt.Errorf("session: %s: unknown state %q", r.Name, r.State)
	}
	seen := make(map[int]bool, len(r.Actions))
	for i, action := range r.Actions {
		if err := action.validate(i); err != nil {
			return fmt.Errorf("session: %s: %w", r.Name, err)
		}
		if seen[action.Seq] {
			return fmt.Errorf("session: %s: duplicate seq %d", r.Name, action.Seq)
		}
		seen[action.Seq] = true
	}
	return nil
}

// validate checks one action's enums and sequence number.
func (a Action) validate(index int) error {
	switch a.Type {
	case TypeMount, TypeUmount:
	default:
		return fmt.Errorf("action %d: unknown type %q", index, a.Type)
	}
	switch a.Status {
	case StatusPending, StatusMounted, StatusFailed, StatusUnmounted:
	default:
		return fmt.Errorf("action %d: unknown status %q", index, a.Status)
	}
	switch a.Mode {
	case "", ModeRO, ModeRW:
	default:
		return fmt.Errorf("action %d: unknown mode %q", index, a.Mode)
	}
	if a.Type == TypeMount && a.Mode == "" {
		return fmt.Errorf("action %d: a mount requires mode ro or rw", index)
	}
	if a.Seq < 1 {
		return fmt.Errorf("action %d: seq must be positive", index)
	}
	return nil
}
