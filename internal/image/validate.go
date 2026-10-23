package image

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jiangxin/tiler/internal/container"
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
	checkNameField(m.Name, list)
	if m.Kind != KindFull && m.Kind != KindIncremental {
		list.add("kind: must be full or incremental")
	}
	if m.Builder == "" {
		list.add("builder: must be set")
	}
	checkCommit("source.commit", m.Source.Commit, list)
}

// checkNameField requires Name to parse as a canonical 1-to-5
// segment library name that round-trips through ParseName.
func checkNameField(name string, list *issueList) {
	segs, err := ParseName(name)
	if err != nil || strings.Join(segs, "/") != name {
		list.add("name: invalid image name")
	}
}

// checkPayload checks the active container and the payload facts.
// The active format covers the chain: layers do not store a separate
// type or compression, so every member stays on this container.
func (m Meta) checkPayload(list *issueList) {
	checkContainer(m.Format, list)
	checkSize("size", m.Size, list)
	checkMtime("mtime_unix", m.MtimeUnix, list)
	checkTime("created_at", m.CreatedAt, list)
	if m.ContentSHA256 != "" {
		checkSum("content_sha256", m.ContentSHA256, list)
	}
}

// checkContainer accepts a registered type and a compression that
// type allows. An empty compression is not a stored default; meta
// records the concrete name.
func checkContainer(format Format, list *issueList) {
	// dmg metadata is readable on a host that did not register the
	// format, so a library copied to Linux can still be shown.
	if format.Type == FormatDMG {
		if !container.StoredDMGCompression(format.Compression) {
			list.add("format.compression: dmg does not allow %q", format.Compression)
		}
		return
	}
	f, err := container.Lookup(format.Type)
	if err != nil {
		list.add("format.type: %v", err)
		return
	}
	if format.Compression == "" || !f.AllowsCompression(format.Compression) {
		list.add("format.compression: %s does not allow %q", f.Type(), format.Compression)
	}
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
// carry a chain whose entries each hold their own Blocks list.
// Historical members-form metadata is refused fail-closed.
func (m Meta) checkSplit(list *issueList) {
	if len(m.Members) > 0 {
		list.add("members: the members split form is no longer supported")
	}
	if len(m.Blocks) == 0 {
		return
	}
	if m.Kind != KindFull {
		list.add("kind: must be full for split payloads")
	}
	checkBlockList("blocks", m.Blocks, list)
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
