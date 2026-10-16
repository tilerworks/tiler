package image

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Validate reports every rule violation in one error.
func (m Meta) Validate() error {
	var list issueList
	m.checkIdentity(&list)
	m.checkPayload(&list)
	m.checkChain(&list)
	return list.err()
}

func (m Meta) checkIdentity(list *issueList) {
	checkDigest(m.ID, m.SHA256, "", list)
	checkSegmentField("owner", m.Owner, list)
	checkSegmentField("repo", m.Repo, list)
	if m.Kind != KindFull && m.Kind != KindIncremental {
		list.add("kind: must be full or incremental")
	}
	if m.Builder == "" {
		list.add("builder: must be set")
	}
	checkCommit("source.commit", m.Source.Commit, list)
}

func (m Meta) checkPayload(list *issueList) {
	if m.Format.Type != FormatSquashfs {
		list.add("format.type: must be squashfs")
	}
	if m.Format.Compression != CompressionZstd {
		list.add("format.compression: must be zstd")
	}
	checkSize("size", m.Size, list)
	checkMtime("mtime_unix", m.MtimeUnix, list)
	checkTime("created_at", m.CreatedAt, list)
}

func (m Meta) checkChain(list *issueList) {
	seen := map[string]struct{}{}
	if m.ID != "" {
		seen[m.ID] = struct{}{}
	}
	parent := m.ID
	for i := range m.Chain {
		entry := &m.Chain[i]
		prefix := fmt.Sprintf("chain[%d]", i)
		checkDigest(entry.ID, entry.SHA256, prefix, list)
		checkSize(prefix+".size", entry.Size, list)
		checkMtime(prefix+".mtime_unix", entry.MtimeUnix, list)
		checkTime(prefix+".created_at", entry.CreatedAt, list)
		checkCommit(prefix+".source.commit", entry.Source.Commit, list)
		if entry.Parent != parent {
			list.add("%s.parent: must continue the linear chain", prefix)
		}
		noteDuplicate(prefix, entry.ID, seen, list)
		parent = entry.ID
	}
}

func checkDigest(id, sum, prefix string, list *issueList) {
	idName := joinField(prefix, "id")
	sumName := joinField(prefix, "sha256")
	if !isLowerHex(id, idHexLen) {
		list.add("%s: must be 16 lowercase hex digits", idName)
	}
	if !isLowerHex(sum, sumHexLen) {
		list.add("%s: must be 64 lowercase hex digits", sumName)
	}
	if isLowerHex(sum, sumHexLen) && id != sum[:idHexLen] {
		list.add("%s: must be the first 16 hex digits of sha256", idName)
	}
}

func checkSegmentField(field, seg string, list *issueList) {
	if err := checkSegment(seg); err != nil {
		list.add("%s: invalid segment", field)
	}
}

func checkSize(field string, size int64, list *issueList) {
	if size <= 0 {
		list.add("%s: must be greater than 0", field)
	}
}

func checkMtime(field string, unix int64, list *issueList) {
	if unix < 0 {
		list.add("%s: must not be negative", field)
	}
}

func checkTime(field string, ts time.Time, list *issueList) {
	if ts.IsZero() {
		list.add("%s: must be set", field)
		return
	}
	if _, offset := ts.Zone(); offset != 0 {
		list.add("%s: must be UTC", field)
	}
}

func checkCommit(field, commit string, list *issueList) {
	if commit == "" {
		return
	}
	if !isLowerHex(commit, len(commit)) {
		list.add("%s: must be lowercase hex", field)
	}
}

func noteDuplicate(prefix, id string, seen map[string]struct{}, list *issueList) {
	if id == "" {
		return
	}
	if _, ok := seen[id]; ok {
		list.add("%s.id: duplicate", prefix)
	}
	seen[id] = struct{}{}
}

func joinField(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}

func isLowerHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

type issueList struct {
	items []string
}

func (list *issueList) add(format string, args ...any) {
	list.items = append(list.items, fmt.Sprintf(format, args...))
}

func (list *issueList) err() error {
	if len(list.items) == 0 {
		return nil
	}
	return errors.New(strings.Join(list.items, "; "))
}
