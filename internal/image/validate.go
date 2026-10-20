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
	m.checkSplit(&list)
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
	blocksForm := len(m.Blocks) > 0
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
		switch {
		case blocksForm && len(entry.Blocks) == 0:
			list.add("%s.blocks: must be set for blocks payloads", prefix)
		case !blocksForm && len(entry.Blocks) > 0:
			list.add("%s.blocks: must be empty for single payloads", prefix)
		}
		checkBlockList(prefix+".blocks", entry.Blocks, list)
		noteDuplicate(prefix, entry.ID, seen, list)
		parent = entry.ID
	}
}

// checkSplit validates the split payload manifests. The single-file
// form carries no manifest, so its rules stay untouched. Blocks may
// carry a chain whose entries each hold their own Blocks list;
// members still forbid a chain.
func (m Meta) checkSplit(list *issueList) {
	if len(m.Blocks) == 0 && len(m.Members) == 0 {
		return
	}
	if len(m.Blocks) > 0 && len(m.Members) > 0 {
		list.add("blocks and members: mutually exclusive")
	}
	if m.Kind != KindFull {
		list.add("kind: must be full for split payloads")
	}
	if len(m.Members) > 0 && len(m.Chain) > 0 {
		list.add("chain: must be empty for members payloads")
	}
	checkBlockList("blocks", m.Blocks, list)
	m.checkMembers(list)
}

// checkBlockList validates one byte-block manifest: names follow the
// blk.NNN sequence in order, every entry records a positive size and
// a lowercase sha256, and the manifest fits the naming capacity.
func checkBlockList(field string, blocks []BlockEntry, list *issueList) {
	if len(blocks) == 0 {
		return
	}
	if len(blocks) > maxBlocks {
		list.add("%s: must hold at most %d entries", field, maxBlocks)
	}
	for i := range blocks {
		entry := &blocks[i]
		prefix := fmt.Sprintf("%s[%d]", field, i)
		if want := fmt.Sprintf(blockNameFmt, i); entry.Name != want {
			list.add("%s.name: must be %s", prefix, want)
		}
		checkSize(prefix+".size", entry.Size, list)
		checkSum(prefix+".sha256", entry.SHA256, list)
	}
}

// checkMembers validates the directory-partition manifest. The
// whole-image digest and size are only compared when every entry
// passed its own checks, so one broken entry is not reported twice.
func (m Meta) checkMembers(list *issueList) {
	if len(m.Members) == 0 {
		return
	}
	clean := true
	for i := range m.Members {
		before := len(list.items)
		m.checkMemberEntry(i, list)
		if len(list.items) != before {
			clean = false
		}
	}
	if !clean {
		return
	}
	if isLowerHex(m.SHA256, sumHexLen) && m.SHA256 != membersDigest(m.Members) {
		list.add("sha256: must equal the derived members manifest digest")
	}
	var total int64
	for i := range m.Members {
		total += m.Members[i].Size
	}
	if m.Size != total {
		list.add("size: must equal the sum of member sizes")
	}
}

// checkMemberEntry validates one manifest member: a legal unique
// sorted name segment, the derived image path, and the recorded
// facts.
func (m Meta) checkMemberEntry(i int, list *issueList) {
	entry := &m.Members[i]
	prefix := fmt.Sprintf("members[%d]", i)
	if err := checkSegment(entry.Name); err != nil {
		list.add("%s.name: invalid segment", prefix)
	} else if i > 0 {
		switch prev := m.Members[i-1].Name; {
		case entry.Name == prev:
			list.add("%s.name: duplicate", prefix)
		case entry.Name < prev:
			list.add("%s.name: must be sorted by name", prefix)
		}
	}
	if want := memberImageName(entry.Name); entry.Image != want {
		list.add("%s.image: must be %s", prefix, want)
	}
	checkSize(prefix+".size", entry.Size, list)
	checkSum(prefix+".sha256", entry.SHA256, list)
	checkMtime(prefix+".mtime_unix", entry.MtimeUnix, list)
}

func checkDigest(id, sum, prefix string, list *issueList) {
	idName := joinField(prefix, "id")
	if !isLowerHex(id, idHexLen) {
		list.add("%s: must be 16 lowercase hex digits", idName)
	}
	checkSum(joinField(prefix, "sha256"), sum, list)
	if isLowerHex(sum, sumHexLen) && id != sum[:idHexLen] {
		list.add("%s: must be the first 16 hex digits of sha256", idName)
	}
}

func checkSum(field, sum string, list *issueList) {
	if !isLowerHex(sum, sumHexLen) {
		list.add("%s: must be 64 lowercase hex digits", field)
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
