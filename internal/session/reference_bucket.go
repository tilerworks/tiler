package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jiangxin/tiler/internal/image"
)

const referenceDir = ".refs"

// ReferenceOwner identifies one retained mount role in one session incarnation.
type ReferenceOwner struct {
	Session    string `json:"session"`
	InstanceID string `json:"instance_id"`
	ActionID   int    `json:"action_id"`
	Role       string `json:"role"`
}

// ReferenceBucket is derived data, never authoritative proof of no references.
type ReferenceBucket struct {
	Name   string           `json:"name"`
	Tip    string           `json:"tip"`
	Owners []ReferenceOwner `json:"owners"`
}

func (b ReferenceBucket) validate() error {
	parts, err := image.ParseName(b.Name)
	if err != nil || strings.Join(parts, "/") != b.Name || !completeHash(b.Tip) {
		return fmt.Errorf("session: invalid reference bucket identity")
	}
	seen := make(map[ReferenceOwner]bool)
	for _, owner := range b.Owners {
		if ValidateName(owner.Session) != nil || !completeHash(owner.InstanceID) || owner.ActionID < 1 ||
			(owner.Role != "desired" && owner.Role != "served") || seen[owner] {
			return fmt.Errorf("session: invalid or duplicate reference owner")
		}
		seen[owner] = true
	}
	return nil
}

func (s Store) referenceBucketPath(name, tip string) (string, error) {
	if err := (ReferenceBucket{Name: name, Tip: tip}).validate(); err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(name))
	return s.referencePath(hex.EncodeToString(digest[:]), tip+".json")
}

func (s Store) referencePath(parts ...string) (string, error) {
	path := filepath.Join(append([]string{s.Dir, referenceDir}, parts...)...)
	return s.safeStoreFile(path)
}

func (s Store) safeStoreFile(path string) (string, error) {
	if strings.TrimSpace(s.Dir) == "" {
		return "", fmt.Errorf("session: empty reference store directory")
	}
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if err == nil && (info.Mode()&os.ModeSymlink != 0 ||
			(current == path && !info.Mode().IsRegular()) || (current != path && !info.IsDir())) {
			return "", fmt.Errorf("session: unsafe reference path %s", current)
		}
		if current == filepath.Clean(s.Dir) {
			return path, nil
		}
	}
}

// LoadReferenceBucket reads a shard; a missing shard returns os.ErrNotExist.
// Callers coordinate I/O with the library lock and establish index trust first.
func (s Store) LoadReferenceBucket(name, tip string) (ReferenceBucket, error) {
	path, err := s.referenceBucketPath(name, tip)
	if err != nil {
		return ReferenceBucket{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ReferenceBucket{}, err
	}
	var bucket ReferenceBucket
	if err := json.Unmarshal(data, &bucket); err != nil {
		return bucket, fmt.Errorf("session: parse %s: %w", path, err)
	}
	if bucket.Name != name || bucket.Tip != tip {
		return bucket, fmt.Errorf("session: mismatched reference bucket %s", path)
	}
	return bucket, bucket.validate()
}

// SaveReferenceBucket atomically replaces one validated shard under the library
// lock. Session records remain authoritative; transaction coordination is external.
func (s Store) SaveReferenceBucket(bucket ReferenceBucket) error {
	if err := bucket.validate(); err != nil {
		return err
	}
	path, err := s.referenceBucketPath(bucket.Name, bucket.Tip)
	if err != nil {
		return err
	}
	bucket.Owners = slices.Clone(bucket.Owners)
	slices.SortFunc(bucket.Owners, compareReferenceOwners)
	data, err := json.MarshalIndent(bucket, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return writeAtomic(path, append(data, '\n'))
}
