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

// Validate reports every manifest rule violation in one error. The
// graph rules mirror the v1 chain rules: unique well-formed ids,
// exactly one parentless full root, every other node parented
// inside the graph, no cycles, a head inside the graph, root-to-node
// paths within the stack limit, and one payload kind across nodes.
// Every node also carries the v1 payload facts — digest, size,
// mtime, and created_at — and the full root carries the v1 root
// facts: the builder and, when recorded, the content digest.
func (m Manifest) Validate() error {
	var list issueList
	if m.Version != ManifestVersion {
		list.add("version: unsupported metadata version %q", m.Version)
	}
	checkNameField(m.Name, &list)
	checkContainer(m.Format, &list)
	index := m.checkImages(&list)
	root := m.checkEdges(index, &list)
	m.checkBlocksForm(root, &list)
	m.checkHead(index, &list)
	m.checkWalks(index, &list)
	return list.err()
}

// checkImages validates the per-node fields and returns the map
// from a well-formed unique id to its node index.
func (m Manifest) checkImages(list *issueList) map[string]int {
	index := make(map[string]int, len(m.Images))
	for i := range m.Images {
		node := &m.Images[i]
		prefix := fmt.Sprintf("images[%d]", i)
		if !onDiskID(node.ID) {
			list.add("%s.id: must be %d lowercase hex digits, or a legacy %d-digit id",
				prefix, sumHexLen, idHexLen)
		} else if _, ok := index[node.ID]; ok {
			list.add("%s.id: duplicate", prefix)
		} else {
			index[node.ID] = i
		}
		if node.Kind != KindFull && node.Kind != KindIncremental {
			list.add("%s.kind: must be full or incremental", prefix)
		}
		if node.Parent != "" && !onDiskID(node.Parent) {
			list.add("%s.parent: must be %d lowercase hex digits, or a legacy %d-digit id",
				prefix, sumHexLen, idHexLen)
		}
		checkNodeFacts(prefix, node, list)
		checkBlockList(prefix+".blocks", node.Blocks, list)
	}
	return index
}

// checkNodeFacts enforces the v1 per-node payload facts on one
// manifest node. The digest fact mirrors checkDigest with the
// legacy on-disk id tolerance instead of the strict 64-hex sum:
// sha256 must be well-formed and equal the node id. The builder
// and content_sha256 facts stay on the full root only, because
// v1 chain entries carry neither.
func checkNodeFacts(prefix string, node *ImageMeta, list *issueList) {
	if !onDiskID(node.SHA256) {
		list.add("%s.sha256: must be %d lowercase hex digits, or a legacy %d-digit id",
			prefix, sumHexLen, idHexLen)
	} else if node.SHA256 != node.ID {
		list.add("%s.id: must equal sha256", prefix)
	}
	checkSize(prefix+".size", node.Size, list)
	checkMtime(prefix+".mtime_unix", node.MtimeUnix, list)
	checkTime(prefix+".created_at", node.CreatedAt, list)
	if node.Kind != KindFull {
		return
	}
	if node.Builder == "" {
		list.add("%s.builder: must be set", prefix)
	}
	if node.ContentSHA256 != "" {
		checkSum(prefix+".content_sha256", node.ContentSHA256, list)
	}
}

// checkEdges validates the node kinds and the parent edges. It
// returns the index of the full root, or -1 when the graph has none.
func (m Manifest) checkEdges(index map[string]int, list *issueList) int {
	root := -1
	for i := range m.Images {
		node := &m.Images[i]
		prefix := fmt.Sprintf("images[%d]", i)
		switch node.Kind {
		case KindFull:
			if root >= 0 {
				list.add("%s.kind: more than one full root", prefix)
			} else {
				root = i
			}
			if node.Parent != "" {
				list.add("%s.parent: the full root must not have a parent", prefix)
			}
		case KindIncremental:
			if node.Parent == "" {
				list.add("%s.parent: incremental nodes must have a parent", prefix)
			} else if _, ok := index[node.Parent]; !ok {
				list.add("%s.parent: unknown image id", prefix)
			}
		}
	}
	if root < 0 {
		list.add("images: must hold exactly one full root")
	}
	return root
}

// checkBlocksForm keeps every node on the payload kind of the full
// root: the split form stores blocks for every node, the single
// form stores none.
func (m Manifest) checkBlocksForm(root int, list *issueList) {
	if root < 0 {
		return
	}
	blocksForm := len(m.Images[root].Blocks) > 0
	for i := range m.Images {
		node := &m.Images[i]
		prefix := fmt.Sprintf("images[%d]", i)
		switch {
		case blocksForm && len(node.Blocks) == 0:
			list.add("%s.blocks: must be set for blocks payloads", prefix)
		case !blocksForm && len(node.Blocks) > 0:
			list.add("%s.blocks: must be empty for single payloads", prefix)
		}
	}
}

// checkHead requires head to name a node of the graph.
func (m Manifest) checkHead(index map[string]int, list *issueList) {
	if m.Head == "" {
		list.add("head: must be set")
		return
	}
	if _, ok := index[m.Head]; !ok {
		list.add("head: unknown image id")
	}
}

// checkWalks reports the first parent cycle once and every node
// whose root-to-node path exceeds the stack limit.
func (m Manifest) checkWalks(index map[string]int, list *issueList) {
	cycleReported := false
	for i := range m.Images {
		prefix := fmt.Sprintf("images[%d]", i)
		onPath := make(map[string]bool, len(m.Images))
		depth := 0
		cycle := false
		for id := m.Images[i].ID; id != ""; {
			if onPath[id] {
				cycle = true
				break
			}
			onPath[id] = true
			depth++
			j, ok := index[id]
			if !ok {
				break // a malformed id was already reported
			}
			id = m.Images[j].Parent
		}
		switch {
		case cycle && !cycleReported:
			list.add("%s.parent: the parent edges must stay acyclic", prefix)
			cycleReported = true
		case !cycle && depth > maxStackDepth:
			list.add("%s: root path of %d images exceeds the stack limit of %d",
				prefix, depth, maxStackDepth)
		}
	}
}

func (m Meta) checkIdentity(list *issueList) {
	checkDigest(m.ID, m.SHA256, "", list)
	checkNameField(m.Name, list)
	if m.Version != MetaVersion {
		list.add("version: unsupported metadata version %q", m.Version)
	}
	if m.Kind != KindFull && m.Kind != KindIncremental {
		list.add("kind: must be full or incremental")
	}
	if m.Builder == "" {
		list.add("builder: must be set")
	}
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
func (m Meta) checkSplit(list *issueList) {
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
	if !isLowerHex(id, sumHexLen) {
		list.add("%s: must be 64 lowercase hex digits", idName)
	}
	checkSum(joinField(prefix, "sha256"), sum, list)
	if isLowerHex(sum, sumHexLen) && id != sum {
		list.add("%s: must equal sha256", idName)
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
