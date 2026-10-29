package image

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// CopyPublish is one committed copy import. Items reuse destination
// metadata when a version was not added. A pure promotion keeps that
// metadata byte for byte and records completion on the journal phase.
type CopyPublish struct {
	Name  string
	Items []ImportMergeItem
}

// copyPublishSeam is the in-process failure point for one copy batch.
// failID rejects that version before its leaves are replaced. failLeaf
// rejects the 1-based promotion replacement. halt leaves a phase durable.
type copyPublishSeam struct {
	failID   string
	failLeaf int
	halt     string
}

type stagedNode struct {
	node     ImageMeta
	payloads []ImportPayload
	files    []string
	extra    []string
	drop     string
	// prior is the unknown id this copy replaced with the real digest.
	prior string
}

type stagedBatch struct {
	root  string
	nodes []stagedNode
}

// PublishCopiedImport copies one verified batch into the target library.
// A same-source known link becomes a local payload with its id, recorded
// mtime, and metadata kept. An existing local copy is left untouched.
// An unknown full root is published under the digest of the copied bytes.
// Parents in this batch that pointed at that root follow the new id.
// Committed zero nodes, their descendants, and session records stay.
func PublishCopiedImport(req ImportRequest) (CopyPublish, error) {
	return publishCopied(req, copyPublishSeam{})
}

func publishCopied(req ImportRequest, seam copyPublishSeam) (CopyPublish, error) {
	if req.Mode != ImportCopy {
		return CopyPublish{}, importFail(req, errors.New("copy publication requires copy mode"))
	}
	read, err := readImportSource(req)
	if err != nil {
		return CopyPublish{}, err
	}
	batch, err := stageCopyBatch(req, read)
	if err != nil {
		return CopyPublish{}, err
	}
	defer batch.release()
	release, err := LockLibrary(req.ImagesDir, DefaultLibraryLockTimeout)
	if err != nil {
		return CopyPublish{}, importFail(req, err)
	}
	defer release()
	return publishCopiedLocked(req, read, batch, seam)
}

func publishCopiedLocked(req ImportRequest, read importRead, batch *stagedBatch, seam copyPublishSeam) (CopyPublish, error) {
	if err := PrepareTargetLocked(req.ImagesDir); err != nil {
		return CopyPublish{}, err
	}
	if err := checkNamePlacement(req.ImagesDir, read.plan.Repo.Segments); err != nil {
		return CopyPublish{}, importFail(req, err)
	}
	cands := batch.candidates()
	if err := linkSourceStable(read, cands); err != nil {
		return CopyPublish{}, importFail(req, err)
	}
	manifest, merge, err := planLockedCopy(read.plan.Repo, cands)
	if err != nil {
		return CopyPublish{}, err
	}
	if !copyMutates(merge) {
		return CopyPublish{Name: read.plan.Repo.Name(), Items: merge.Items}, nil
	}
	return commitCopyBatch(req, read, manifest, merge, batch, seam)
}

func planLockedCopy(repo Repo, cands []ImportCandidate) (VersionManifest, ImportMerge, error) {
	manifest, _, err := loadVersionMeta(repo)
	if err != nil {
		return VersionManifest{}, ImportMerge{}, err
	}
	merge, err := CheckImportCollisions(repo, ImportCopy, manifest, cands)
	if err != nil {
		return VersionManifest{}, ImportMerge{}, err
	}
	for _, item := range merge.Items {
		switch item.Action {
		case MergeAdd, MergeNoop, MergePromote:
		default:
			return VersionManifest{}, ImportMerge{}, fmt.Errorf("import copy: cannot %s", item.Action)
		}
	}
	return manifest, merge, nil
}

func copyMutates(merge ImportMerge) bool {
	for _, item := range merge.Items {
		if item.Action == MergeAdd || item.Action == MergePromote {
			return true
		}
	}
	return false
}

func (b stagedBatch) candidates() []ImportCandidate {
	out := make([]ImportCandidate, len(b.nodes))
	for i, node := range b.nodes {
		out[i] = ImportCandidate{Node: node.node, Payloads: node.payloads}
	}
	return out
}

func (b *stagedBatch) release() {
	if b == nil {
		return
	}
	for i := range b.nodes {
		node := &b.nodes[i]
		for _, file := range append(node.files, node.extra...) {
			discardStage(file, b.root)
		}
		discardStage(node.drop, b.root)
	}
}

func stageCopyBatch(req ImportRequest, read importRead) (*stagedBatch, error) {
	batch := &stagedBatch{root: req.ImagesDir}
	if read.plan.Kind == SourceFile {
		node, err := stageCopiedFile(req, read.plan)
		if err != nil {
			return nil, importFail(req, err)
		}
		batch.nodes = []stagedNode{node}
		return batch, nil
	}
	selected, err := selectImportClosure(read.manifest, req.Hash)
	if err != nil {
		return nil, importFail(req, err)
	}
	for _, node := range selected {
		one, stageErr := stageCopiedNode(req, read, node)
		if stageErr != nil {
			batch.release()
			return nil, importFail(req, stageErr)
		}
		batch.nodes = append(batch.nodes, one)
	}
	rekeyCopiedParents(batch.nodes)
	if err = metaUnchanged(read); err != nil {
		batch.release()
		return nil, importFail(req, err)
	}
	return batch, nil
}

func stageCopiedFile(req ImportRequest, plan SourcePlan) (stagedNode, error) {
	staged, err := copyImportFile(req, plan, stageCopyHooks{})
	if err != nil {
		return stagedNode{}, err
	}
	node := ImageMeta{
		ID: staged.SHA256, Kind: KindFull, Format: staged.Format, Size: staged.Size,
		SHA256: staged.SHA256, MtimeUnix: staged.MtimeUnix, CreatedAt: linkNow().UTC(),
		Builder: importBuilder,
	}
	payload := ImportPayload{Source: plan.Location, ContentOK: true}
	return stagedNode{node: node, payloads: []ImportPayload{payload}, files: []string{staged.Path}}, nil
}

func stageCopiedNode(req ImportRequest, read importRead, node ImageMeta) (stagedNode, error) {
	node.Blocks = slices.Clone(node.Blocks)
	if unknownRootRejected(node) {
		return stagedNode{}, errors.New("reserved unknown digest")
	}
	if len(node.Blocks) == 0 {
		return stageDirectoryFile(req, read, node)
	}
	return stageDirectoryBlocks(req, read, node)
}

func unknownRootRejected(node ImageMeta) bool {
	unknown := IsUnknownSHA256(node.ID) || IsUnknownSHA256(node.SHA256)
	if !unknown {
		return false
	}
	return node.Kind != KindFull || len(node.Blocks) != 0 || node.Parent != "" || node.ID != node.SHA256
}

func stageDirectoryFile(req ImportRequest, read importRead, node ImageMeta) (stagedNode, error) {
	loc, err := canonicalPayload(Repo{Dir: read.plan.Location}, node, "")
	if err != nil {
		return stagedNode{}, err
	}
	plan := read.plan
	plan.Kind = SourceFile
	plan.Location = loc
	staged, err := copyImportFile(req, plan, stageCopyHooks{})
	if err != nil {
		return stagedNode{}, err
	}
	if !directoryCopyMatches(node, staged) {
		staged.Remove()
		return stagedNode{}, errors.New("content conflict")
	}
	prior := ""
	if IsUnknownSHA256(node.ID) {
		prior = node.ID
		node.ID = staged.SHA256
		node.SHA256 = staged.SHA256
	}
	payload := ImportPayload{Source: loc, ContentOK: true}
	return stagedNode{
		node: node, prior: prior, payloads: []ImportPayload{payload}, files: []string{staged.Path},
	}, nil
}

func directoryCopyMatches(node ImageMeta, staged StagedCopy) bool {
	facts := staged.Size == node.Size && staged.Format == node.Format && staged.MtimeUnix == node.MtimeUnix
	if IsUnknownSHA256(node.ID) {
		return facts
	}
	return facts && staged.SHA256 == node.SHA256
}

func rekeyCopiedParents(nodes []stagedNode) {
	var prior, next string
	for i := range nodes {
		if nodes[i].prior == "" {
			continue
		}
		prior, next = nodes[i].prior, nodes[i].node.ID
		break
	}
	if next == "" {
		return
	}
	for i := range nodes {
		if nodes[i].node.Parent == prior {
			nodes[i].node.Parent = next
		}
	}
}

func stageDirectoryBlocks(req ImportRequest, read importRead, node ImageMeta) (stagedNode, error) {
	staged, err := copyImportBlocks(context.Background(), req, read, node, stageCopyHooks{})
	if err != nil {
		return stagedNode{}, err
	}
	one, err := blockStageNode(read.plan.Location, node, staged)
	if err != nil {
		staged.Remove()
		return stagedNode{}, err
	}
	return one, nil
}

func blockStageNode(source string, node ImageMeta, staged StagedBlocks) (stagedNode, error) {
	src := Repo{Dir: source}
	payloads := make([]ImportPayload, len(node.Blocks))
	files := make([]string, len(node.Blocks))
	for i, block := range node.Blocks {
		if staged.Blocks[i].SHA256 != block.SHA256 || staged.Blocks[i].MtimeUnix != block.MtimeUnix {
			return stagedNode{}, errors.New("content conflict")
		}
		loc, err := canonicalPayload(src, node, block.Name)
		if err != nil {
			return stagedNode{}, err
		}
		payloads[i] = ImportPayload{Block: block.Name, Source: loc, ContentOK: true}
		files[i] = filepath.Join(staged.Dir, block.Name)
	}
	extra := []string{filepath.Join(staged.Dir, fileConcatView)}
	return stagedNode{node: node, payloads: payloads, files: files, extra: extra, drop: staged.Dir}, nil
}

func canonicalPayload(src Repo, node ImageMeta, block string) (string, error) {
	var leaf string
	var err error
	if block == "" {
		leaf, err = src.ObjectPayloadPath(node)
	} else {
		leaf, err = src.ObjectBlockPath(node.ID, block)
	}
	if err != nil {
		return "", err
	}
	resolved, err := ResolvePayloadBytesPath(leaf)
	if err != nil {
		return "", err
	}
	return canonicalPath(resolved)
}

func commitCopyBatch(req ImportRequest, read importRead, manifest VersionManifest, merge ImportMerge, batch *stagedBatch, seam copyPublishSeam) (CopyPublish, error) {
	repo := read.plan.Repo
	txn, err := planCopyTxn(repo, manifest, merge, batch.nodes)
	if err != nil {
		return CopyPublish{}, importFail(req, err)
	}
	if err = linkObjectsFree(repo, txn.Objects); err != nil {
		return CopyPublish{}, importFail(req, err)
	}
	if err = SaveImportTxn(req.ImagesDir, txn); err != nil {
		return CopyPublish{}, importFail(req, err)
	}
	if err = applyCopyFiles(repo, merge, batch.nodes, txn.Backups, seam); err != nil {
		return abortCopied(req, err)
	}
	if seam.halt == "objects" {
		return CopyPublish{}, errLinkHalted
	}
	if err = linkSourceStable(read, batch.candidates()); err != nil {
		return abortCopied(req, err)
	}
	return finishCopyBatch(req, repo, txn, merge, seam)
}

func finishCopyBatch(req ImportRequest, repo Repo, txn ImportTxn, merge ImportMerge, seam copyPublishSeam) (CopyPublish, error) {
	before, err := EncodeVersionManifest(txn.Before)
	if err != nil {
		return abortCopied(req, err)
	}
	after, err := EncodeVersionManifest(txn.After)
	if err != nil {
		return abortCopied(req, err)
	}
	if !bytes.Equal(before, after) {
		if err = commitLinkManifest(repo, txn.After); err != nil {
			return abortCopied(req, err)
		}
	}
	if seam.halt == "meta" {
		return CopyPublish{}, errLinkHalted
	}
	txn.Phase = ImportCommitted
	if err = SaveImportTxn(req.ImagesDir, txn); err != nil {
		return abortCopied(req, err)
	}
	if err = finishLinkIndexes(repo, linkPublishSeam{halt: seam.halt}); err != nil {
		return CopyPublish{}, err
	}
	published := CopyPublish{Name: repo.Name(), Items: merge.Items}
	if err = RecoverImportTxn(req.ImagesDir); err != nil {
		return published, err
	}
	return published, nil
}

func planCopyTxn(repo Repo, manifest VersionManifest, merge ImportMerge, nodes []stagedNode) (ImportTxn, error) {
	cands := make([]ImportCandidate, len(nodes))
	for i, node := range nodes {
		cands[i] = ImportCandidate{Node: node.node}
	}
	after, ids, err := linkAfter(manifest, merge, cands)
	if err != nil {
		return ImportTxn{}, err
	}
	staged, backups, err := copyJournalPaths(repo, merge, nodes)
	if err != nil {
		return ImportTxn{}, err
	}
	return ImportTxn{
		Name: repo.Name(), Phase: ImportPrepared, Before: manifest, After: after,
		Staged: staged, Objects: ids, Backups: backups,
	}, nil
}

func copyJournalPaths(repo Repo, merge ImportMerge, nodes []stagedNode) ([]string, []ImportBackup, error) {
	var staged []string
	var backups []ImportBackup
	n := 0
	for i, item := range merge.Items {
		if item.Action == MergeNoop {
			continue
		}
		rels, err := repoRels(repo, append(append([]string{}, nodes[i].files...), nodes[i].extra...))
		if err != nil {
			return nil, nil, err
		}
		staged = append(staged, rels...)
		if item.Action != MergePromote {
			continue
		}
		lives, err := promotionLives(repo, item.Node)
		if err != nil {
			return nil, nil, err
		}
		if len(lives) != len(nodes[i].files) {
			return nil, nil, errors.New("promotion leaves do not match the copy")
		}
		for _, live := range lives {
			n++
			rel, relErr := repoRel(repo, live)
			if relErr != nil {
				return nil, nil, relErr
			}
			backups = append(backups, ImportBackup{Live: rel, Saved: fmt.Sprintf("%s/%d", importBackupRel, n)})
		}
	}
	return staged, backups, nil
}

func applyCopyFiles(repo Repo, merge ImportMerge, nodes []stagedNode, backups []ImportBackup, seam copyPublishSeam) error {
	next := 0
	replaced := 0
	for i, item := range merge.Items {
		if item.Action == MergeNoop {
			continue
		}
		if nodes[i].node.ID == seam.failID {
			return fmt.Errorf("version %s: injected", seam.failID)
		}
		var err error
		if item.Action == MergeAdd {
			err = installAddedCopy(repo, nodes[i])
		} else {
			err = promoteKnownLink(repo, nodes[i], backups[next:], &replaced, seam)
			next += len(nodes[i].files)
		}
		if err != nil {
			return err
		}
	}
	return syncDir(repo.Dir, hooks{})
}

func promoteKnownLink(repo Repo, node stagedNode, backups []ImportBackup, replaced *int, seam copyPublishSeam) error {
	for i, file := range node.files {
		*replaced++
		if seam.failLeaf == *replaced {
			return errors.New("injected replacement")
		}
		saved, err := ownedImportPath(repo, backups[i].Saved)
		if err != nil {
			return err
		}
		live, err := ownedImportPath(repo, backups[i].Live)
		if err != nil {
			return err
		}
		if err = moveInto(live, saved); err != nil {
			return err
		}
		if err = moveInto(file, live); err != nil {
			return err
		}
	}
	return nil
}

func installAddedCopy(repo Repo, node stagedNode) error {
	if len(node.node.Blocks) == 0 {
		dest, err := repo.ObjectPayloadPath(node.node)
		if err != nil {
			return err
		}
		return moveInto(node.files[0], dest)
	}
	dir, err := repo.ObjectDirPath(node.node.ID)
	if err != nil {
		return err
	}
	for i, block := range node.node.Blocks {
		dest, destErr := repo.ObjectBlockPath(node.node.ID, block.Name)
		if destErr != nil {
			return destErr
		}
		if err = moveInto(node.files[i], dest); err != nil {
			return err
		}
	}
	return writeConcatView(dir, blockNames(node.node.Blocks))
}

func promotionLives(repo Repo, node ImageMeta) ([]string, error) {
	if len(node.Blocks) == 0 {
		path, err := repo.ObjectPayloadPath(node)
		if err != nil {
			return nil, err
		}
		return []string{path}, nil
	}
	paths := make([]string, len(node.Blocks))
	for i, block := range node.Blocks {
		path, err := repo.ObjectBlockPath(node.ID, block.Name)
		if err != nil {
			return nil, err
		}
		paths[i] = path
	}
	return paths, nil
}

func moveInto(src, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if _, err := os.Lstat(dest); err == nil {
		return fmt.Errorf("payload already exists: %s", dest)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return (hooks{}).do(src, dest)
}

func repoRels(repo Repo, paths []string) ([]string, error) {
	rels := make([]string, len(paths))
	for i, path := range paths {
		rel, err := repoRel(repo, path)
		if err != nil {
			return nil, err
		}
		rels[i] = rel
	}
	return rels, nil
}

func repoRel(repo Repo, abs string) (string, error) {
	rel, err := filepath.Rel(repo.Dir, abs)
	if err != nil {
		return "", err
	}
	rel = filepath.ToSlash(rel)
	if !cleanRel(rel) || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("import: path %s escapes the repository", abs)
	}
	return rel, nil
}

func abortCopied(req ImportRequest, cause error) (CopyPublish, error) {
	if err := RecoverImportTxn(req.ImagesDir); err != nil {
		return CopyPublish{}, errors.Join(cause, err)
	}
	return CopyPublish{}, importFail(req, cause)
}
