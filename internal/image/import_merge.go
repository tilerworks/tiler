package image

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// MergeAction is one planned collision result. Checking does not
// publish metadata or replace payload files.
type MergeAction string

const (
	// MergeAdd inserts a version the destination does not have.
	MergeAdd MergeAction = "add"
	// MergeNoop leaves the destination version and its payload untouched.
	MergeNoop MergeAction = "noop"
	// MergePromote replaces a same-source known link with a verified copy.
	MergePromote MergeAction = "promote"
	// MergeRetain keeps a local copy when the request was a link.
	MergeRetain MergeAction = "retain"
)

// ImportPayload is the canonical external file behind one payload.
// Block is empty for a single-file node. ContentOK reports that a
// copy verified the bytes against the candidate digest.
type ImportPayload struct {
	Block     string
	Source    string
	ContentOK bool
}

// ImportCandidate is one planned version. Callers pass parents first.
type ImportCandidate struct {
	Node     ImageMeta
	Payloads []ImportPayload
}

// ImportMergeItem is the version a later publish would keep. An
// existing id reuses the destination node, so descriptive metadata
// stays. Add carries the candidate.
type ImportMergeItem struct {
	Action MergeAction
	Node   ImageMeta
}

// ImportMerge is empty when any candidate conflicts.
type ImportMerge struct {
	Items []ImportMergeItem
}

type leafKind int

const (
	leafLink leafKind = iota + 1
	leafLocal
)

type destLeaf struct {
	kind   leafKind
	source string
	size   int64
	mtime  int64
	path   string
}

// CheckImportCollisions compares candidates with destination metadata
// and canonical linked sources. It does not modify either tree.
func CheckImportCollisions(repo Repo, mode ImportMode, manifest VersionManifest, candidates []ImportCandidate) (ImportMerge, error) {
	if mode != ImportLink && mode != ImportCopy {
		return ImportMerge{}, mergeFail(repo, mode, "", errors.New("mode must be link or copy"))
	}
	merged := append([]ImageMeta{}, manifest.Images...)
	items := make([]ImportMergeItem, 0, len(candidates))
	for _, cand := range candidates {
		item, err := checkOneCandidate(repo, mode, manifest, cand)
		if err != nil {
			return ImportMerge{}, err
		}
		if err := acceptNewVersion(&merged, item, cand); err != nil {
			return ImportMerge{}, mergeFail(repo, mode, cand.Node.ID, err)
		}
		items = append(items, item)
	}
	return ImportMerge{Items: items}, nil
}

func checkOneCandidate(repo Repo, mode ImportMode, manifest VersionManifest, cand ImportCandidate) (ImportMergeItem, error) {
	if err := validateCandidate(mode, cand); err != nil {
		return ImportMergeItem{}, mergeFail(repo, mode, cand.Node.ID, err)
	}
	if err := checkSharedUnknown(repo, manifest, cand); err != nil {
		return ImportMergeItem{}, mergeFail(repo, mode, cand.Node.ID, err)
	}
	old, found := manifestNode(manifest, cand.Node.ID)
	if !found {
		return ImportMergeItem{Action: MergeAdd, Node: cand.Node}, nil
	}
	action, err := decideExisting(repo, mode, old, cand)
	if err != nil {
		return ImportMergeItem{}, mergeFail(repo, mode, cand.Node.ID, err)
	}
	return ImportMergeItem{Action: action, Node: old}, nil
}

func validateCandidate(mode ImportMode, cand ImportCandidate) error {
	if err := rejectUndefinedUnknown([]ImageMeta{cand.Node}); err != nil {
		return err
	}
	if mode == ImportCopy && IsUnknownSHA256(cand.Node.ID) {
		return errors.New("reserved unknown digest")
	}
	return matchPayloads(cand)
}

func matchPayloads(cand ImportCandidate) error {
	blocks := cand.Node.Blocks
	if len(blocks) == 0 {
		if len(cand.Payloads) == 1 && cand.Payloads[0].Block == "" && cand.Payloads[0].Source != "" {
			return nil
		}
		return errors.New("single-file source is required")
	}
	if len(cand.Payloads) != len(blocks) {
		return errors.New("block sources do not match the manifest")
	}
	for i, block := range blocks {
		if cand.Payloads[i].Block != block.Name || cand.Payloads[i].Source == "" {
			return errors.New("block sources do not match the manifest")
		}
	}
	return nil
}

func checkSharedUnknown(repo Repo, manifest VersionManifest, cand ImportCandidate) error {
	old, leaf, ok := unknownLink(repo, manifest, cand)
	if !ok {
		return nil
	}
	loc, err := canonSource(cand.Payloads[0].Source)
	if err != nil || loc != leaf.source {
		return err
	}
	changed := old.Format != cand.Node.Format || old.Size != cand.Node.Size || old.MtimeUnix != cand.Node.MtimeUnix
	if changed {
		return errors.New("unknown source conflict")
	}
	if err := quickLeaves([]destLeaf{leaf}); err != nil {
		return fmt.Errorf("unknown source conflict: %w", err)
	}
	return nil
}

func unknownLink(repo Repo, manifest VersionManifest, cand ImportCandidate) (ImageMeta, destLeaf, bool) {
	if IsUnknownSHA256(cand.Node.ID) || len(cand.Node.Blocks) != 0 {
		return ImageMeta{}, destLeaf{}, false
	}
	old, found := manifestNode(manifest, UnknownSHA256)
	if !found || len(old.Blocks) != 0 {
		return ImageMeta{}, destLeaf{}, false
	}
	leaves, err := classifyNode(repo, old)
	if err != nil || len(leaves) != 1 || leaves[0].kind != leafLink {
		return ImageMeta{}, destLeaf{}, false
	}
	return old, leaves[0], true
}

func decideExisting(repo Repo, mode ImportMode, old ImageMeta, cand ImportCandidate) (MergeAction, error) {
	if !sameVersionIdentity(old, cand.Node) {
		return "", errors.New("identity conflict")
	}
	leaves, err := classifyNode(repo, old)
	if err != nil {
		return "", fmt.Errorf("payload conflict: %w", err)
	}
	if len(leaves) != len(cand.Payloads) || !uniformLeaves(leaves) {
		return "", errors.New("payload conflict")
	}
	if err := quickLeaves(leaves); err != nil {
		return "", fmt.Errorf("payload conflict: %w", err)
	}
	same, err := sameSources(leaves, cand.Payloads)
	if err != nil {
		return "", err
	}
	if mode == ImportLink {
		return decideLink(old, cand.Node, leaves[0].kind == leafLink, same)
	}
	return decideCopy(old, cand, leaves[0].kind == leafLink, same)
}

func decideLink(old, cand ImageMeta, linked, same bool) (MergeAction, error) {
	if !attributesMatch(old, cand) {
		return "", errors.New("source attribute conflict")
	}
	if linked && !same {
		return "", errors.New("source conflict")
	}
	if linked {
		return MergeNoop, nil
	}
	return MergeRetain, nil
}

func decideCopy(old ImageMeta, cand ImportCandidate, linked, same bool) (MergeAction, error) {
	if linked && !same {
		return "", errors.New("source conflict")
	}
	if !contentVerified(cand) {
		return "", errors.New("content conflict")
	}
	if !linked {
		return MergeNoop, nil
	}
	if !attributesMatch(old, cand.Node) || IsUnknownSHA256(old.ID) {
		return "", errors.New("source attribute conflict")
	}
	return MergePromote, nil
}

func acceptNewVersion(merged *[]ImageMeta, item ImportMergeItem, cand ImportCandidate) error {
	if item.Action != MergeAdd {
		return nil
	}
	*merged = append(*merged, cand.Node)
	_, err := NewForest(*merged)
	return err
}

func classifyNode(repo Repo, node ImageMeta) ([]destLeaf, error) {
	if len(node.Blocks) == 0 {
		path, err := repo.ObjectPayloadPath(node)
		if err != nil {
			return nil, err
		}
		leaf, err := classifyLeaf(path, node.Size, node.MtimeUnix)
		if err != nil {
			return nil, err
		}
		return []destLeaf{leaf}, nil
	}
	leaves := make([]destLeaf, 0, len(node.Blocks))
	for _, block := range node.Blocks {
		path, err := repo.ObjectBlockPath(node.ID, block.Name)
		if err != nil {
			return nil, err
		}
		leaf, err := classifyLeaf(path, block.Size, block.MtimeUnix)
		if err != nil {
			return nil, err
		}
		leaves = append(leaves, leaf)
	}
	return leaves, nil
}

func classifyLeaf(path string, size, mtime int64) (destLeaf, error) {
	leaf := destLeaf{path: path, size: size, mtime: mtime}
	target, linked, err := readExternalLinkTarget(path)
	if err != nil {
		return destLeaf{}, err
	}
	if linked {
		loc, err := canonicalPath(target)
		if err != nil {
			return destLeaf{}, err
		}
		leaf.kind = leafLink
		leaf.source = loc
		return leaf, nil
	}
	info, err := os.Lstat(path)
	if err != nil {
		return destLeaf{}, err
	}
	if !info.Mode().IsRegular() {
		return destLeaf{}, fmt.Errorf("payload %s is not a regular file", path)
	}
	leaf.kind = leafLocal
	return leaf, nil
}

func sameSources(leaves []destLeaf, payloads []ImportPayload) (bool, error) {
	if leaves[0].kind != leafLink {
		return true, nil
	}
	for i, leaf := range leaves {
		loc, err := canonSource(payloads[i].Source)
		if err != nil {
			return false, err
		}
		if loc != leaf.source {
			return false, nil
		}
	}
	return true, nil
}

func attributesMatch(a, b ImageMeta) bool {
	if a.MtimeUnix != b.MtimeUnix || len(a.Blocks) != len(b.Blocks) {
		return false
	}
	for i := range a.Blocks {
		if a.Blocks[i].MtimeUnix != b.Blocks[i].MtimeUnix {
			return false
		}
	}
	return true
}

func contentVerified(cand ImportCandidate) bool {
	for _, payload := range cand.Payloads {
		if !payload.ContentOK {
			return false
		}
	}
	return true
}

func uniformLeaves(leaves []destLeaf) bool {
	for _, leaf := range leaves[1:] {
		if leaf.kind != leaves[0].kind {
			return false
		}
	}
	return leaves[0].kind == leafLink || leaves[0].kind == leafLocal
}

func quickLeaves(leaves []destLeaf) error {
	for _, leaf := range leaves {
		if err := quickCheckFile(leaf.path, leaf.size, leaf.mtime); err != nil {
			return err
		}
	}
	return nil
}

func canonSource(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("source is not absolute")
	}
	return canonicalPath(path)
}

func manifestNode(manifest VersionManifest, id string) (ImageMeta, bool) {
	for i := range manifest.Images {
		if manifest.Images[i].ID == id {
			return manifest.Images[i], true
		}
	}
	return ImageMeta{}, false
}

func mergeFail(repo Repo, mode ImportMode, id string, err error) error {
	if id == "" {
		return fmt.Errorf("import %s: image %s: %w", mode, repo.Name(), err)
	}
	return fmt.Errorf("import %s: image %s: version %s: %w", mode, repo.Name(), id, err)
}
