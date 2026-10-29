package image

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

// importBuilder is the publisher recorded on a single-file link.
// Directory imports keep the source builder instead.
const importBuilder = "import"

// errLinkHalted stops a link publish without rolling the journal back.
// Tests use it to imitate a crash between durable phases.
var errLinkHalted = errors.New("import: halted")

// LinkPublish is one committed link import. Items reuse destination
// metadata for versions that were not added. A batch with no new
// version does not rewrite metadata.
type LinkPublish struct {
	Name  string
	Items []ImportMergeItem
}

// linkPublishSeam is the in-process failure point for one link batch.
// failID rejects that version before its position file is created.
// halt names a phase to leave durable: objects, meta, or index.
type linkPublishSeam struct {
	failID string
	halt   string
}

// PublishLinkedImport writes absolute position files for one link
// batch and replaces metadata once. An all no-op or retain result
// leaves metadata and indexes unchanged. Copy promotion is separate.
func PublishLinkedImport(req ImportRequest) (LinkPublish, error) {
	return publishLinked(req, linkPublishSeam{})
}

func publishLinked(req ImportRequest, seam linkPublishSeam) (LinkPublish, error) {
	if req.Mode != ImportLink {
		return LinkPublish{}, importFail(req, errors.New("link publication requires link mode"))
	}
	read, err := readImportSource(req)
	if err != nil {
		return LinkPublish{}, err
	}
	cands, err := linkCandidates(req, read)
	if err != nil {
		return LinkPublish{}, importFail(req, err)
	}
	release, err := LockLibrary(req.ImagesDir, DefaultLibraryLockTimeout)
	if err != nil {
		return LinkPublish{}, importFail(req, err)
	}
	defer release()
	return publishLinkedLocked(req, read, cands, seam)
}

func publishLinkedLocked(req ImportRequest, read importRead, cands []ImportCandidate, seam linkPublishSeam) (LinkPublish, error) {
	if err := PrepareTargetLocked(req.ImagesDir); err != nil {
		return LinkPublish{}, err
	}
	if err := checkNamePlacement(req.ImagesDir, read.plan.Repo.Segments); err != nil {
		return LinkPublish{}, importFail(req, err)
	}
	if err := linkSourceStable(read, cands); err != nil {
		return LinkPublish{}, importFail(req, err)
	}
	manifest, merge, err := planLockedLink(read.plan.Repo, cands)
	if err != nil {
		return LinkPublish{}, err
	}
	if !linkAdds(merge) {
		return LinkPublish{Name: read.plan.Repo.Name(), Items: merge.Items}, nil
	}
	return commitLinkBatch(req, read, manifest, merge, cands, seam)
}

func planLockedLink(repo Repo, cands []ImportCandidate) (VersionManifest, ImportMerge, error) {
	manifest, _, err := loadVersionMeta(repo)
	if err != nil {
		return VersionManifest{}, ImportMerge{}, err
	}
	merge, err := CheckImportCollisions(repo, ImportLink, manifest, cands)
	if err != nil {
		return VersionManifest{}, ImportMerge{}, err
	}
	if err := acceptedLinkActions(merge); err != nil {
		return VersionManifest{}, ImportMerge{}, err
	}
	return manifest, merge, nil
}

func acceptedLinkActions(merge ImportMerge) error {
	for _, item := range merge.Items {
		switch item.Action {
		case MergeAdd, MergeNoop, MergeRetain:
		default:
			return fmt.Errorf("import link: cannot %s", item.Action)
		}
	}
	return nil
}

func linkAdds(merge ImportMerge) bool {
	for _, item := range merge.Items {
		if item.Action == MergeAdd {
			return true
		}
	}
	return false
}

func commitLinkBatch(req ImportRequest, read importRead, manifest VersionManifest, merge ImportMerge, cands []ImportCandidate, seam linkPublishSeam) (LinkPublish, error) {
	after, ids, err := linkAfter(manifest, merge, cands)
	if err != nil {
		return LinkPublish{}, importFail(req, err)
	}
	repo := read.plan.Repo
	if err := linkObjectsFree(repo, ids); err != nil {
		return LinkPublish{}, importFail(req, err)
	}
	txn := ImportTxn{
		Name: repo.Name(), Phase: ImportPrepared, Before: manifest, After: after, Objects: ids,
	}
	if err := SaveImportTxn(req.ImagesDir, txn); err != nil {
		return LinkPublish{}, importFail(req, err)
	}
	if err := writeLinkBatch(repo, merge, cands, seam.failID); err != nil {
		return abortLinked(req, err)
	}
	if seam.halt == "objects" {
		return LinkPublish{}, errLinkHalted
	}
	if err := linkSourceStable(read, cands); err != nil {
		return abortLinked(req, err)
	}
	if err := commitLinkManifest(repo, after); err != nil {
		return abortLinked(req, err)
	}
	if seam.halt == "meta" {
		return LinkPublish{}, errLinkHalted
	}
	txn.Phase = ImportCommitted
	if err := SaveImportTxn(req.ImagesDir, txn); err != nil {
		return abortLinked(req, err)
	}
	if err := finishLinkIndexes(repo, seam); err != nil {
		return LinkPublish{}, err
	}
	published := LinkPublish{Name: repo.Name(), Items: merge.Items}
	if err := RecoverImportTxn(req.ImagesDir); err != nil {
		return published, err
	}
	return published, nil
}

func finishLinkIndexes(repo Repo, seam linkPublishSeam) error {
	if err := invalidateVersionIndexes(repo); err != nil {
		return pendingImport(err)
	}
	if seam.halt == "index" {
		return errLinkHalted
	}
	if _, err := EnsureVersionIndexes(repo); err != nil {
		return pendingImport(err)
	}
	return nil
}

func abortLinked(req ImportRequest, cause error) (LinkPublish, error) {
	if err := RecoverImportTxn(req.ImagesDir); err != nil {
		return LinkPublish{}, errors.Join(cause, err)
	}
	return LinkPublish{}, importFail(req, cause)
}

func linkAfter(base VersionManifest, merge ImportMerge, cands []ImportCandidate) (VersionManifest, []string, error) {
	after := base
	after.Images = slices.Clone(base.Images)
	ids := make([]string, 0, len(cands))
	for i, item := range merge.Items {
		if item.Action != MergeAdd {
			continue
		}
		node := cands[i].Node
		node.Blocks = slices.Clone(node.Blocks)
		after.Images = append(after.Images, node)
		ids = append(ids, node.ID)
	}
	if err := after.Validate(); err != nil {
		return VersionManifest{}, nil, err
	}
	return after, ids, nil
}

func linkObjectsFree(repo Repo, ids []string) error {
	for _, id := range ids {
		dir, err := repo.ObjectDirPath(id)
		if err != nil {
			return err
		}
		if _, err := os.Lstat(dir); err == nil {
			return fmt.Errorf("version %s payload already exists", id)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func commitLinkManifest(repo Repo, after VersionManifest) error {
	body, err := EncodeVersionManifest(after)
	if err != nil {
		return err
	}
	previous, err := os.ReadFile(repo.MetaPath())
	if errors.Is(err, os.ErrNotExist) {
		previous = nil
	} else if err != nil {
		return err
	}
	if err := os.MkdirAll(repo.Dir, 0o755); err != nil {
		return err
	}
	return replaceVersionMeta(repo, previous, body, hooks{})
}

func linkSourceStable(read importRead, cands []ImportCandidate) error {
	if read.plan.Kind == SourceDir {
		if err := metaUnchanged(read); err != nil {
			return err
		}
	}
	return payloadsStable(read.plan.Repo, cands)
}

func payloadsStable(repo Repo, cands []ImportCandidate) error {
	for _, cand := range cands {
		if len(cand.Node.Blocks) == 0 {
			err := acceptLinkLeaf(repo, cand.Payloads[0].Source, cand.Node.Size, cand.Node.MtimeUnix)
			if err != nil {
				return err
			}
			continue
		}
		for i, payload := range cand.Payloads {
			block := cand.Node.Blocks[i]
			if err := acceptLinkLeaf(repo, payload.Source, block.Size, block.MtimeUnix); err != nil {
				return fmt.Errorf("block %s: %w", block.Name, err)
			}
		}
	}
	return nil
}

func linkCandidates(req ImportRequest, read importRead) ([]ImportCandidate, error) {
	if read.plan.Kind == SourceFile {
		cand, err := fileLinkCandidate(read.plan)
		if err != nil {
			return nil, err
		}
		return []ImportCandidate{cand}, nil
	}
	return dirLinkCandidates(req, read)
}

func fileLinkCandidate(plan SourcePlan) (ImportCandidate, error) {
	if err := acceptProbedLink(plan.Repo, plan.Location, plan.Size, plan.MtimeUnix, plan.Format); err != nil {
		return ImportCandidate{}, err
	}
	node := ImageMeta{
		ID: plan.SHA256, Kind: KindFull, Format: plan.Format, Size: plan.Size,
		SHA256: plan.SHA256, MtimeUnix: plan.MtimeUnix, CreatedAt: linkNow().UTC(),
		Builder: importBuilder,
	}
	return ImportCandidate{Node: node, Payloads: []ImportPayload{{Source: plan.Location}}}, nil
}

func dirLinkCandidates(req ImportRequest, read importRead) ([]ImportCandidate, error) {
	nodes, err := selectImportClosure(read.manifest, req.Hash)
	if err != nil {
		return nil, err
	}
	src := Repo{Dir: read.plan.Location}
	cands := make([]ImportCandidate, 0, len(nodes))
	for _, node := range nodes {
		payloads, err := linkPayloads(src, read.plan.Repo, node)
		if err != nil {
			return nil, fmt.Errorf("version %s: %w", node.ID, err)
		}
		cands = append(cands, ImportCandidate{Node: node, Payloads: payloads})
	}
	return cands, nil
}

func linkPayloads(src, target Repo, node ImageMeta) ([]ImportPayload, error) {
	if len(node.Blocks) == 0 {
		leaf, err := src.ObjectPayloadPath(node)
		if err != nil {
			return nil, err
		}
		loc, err := resolvedLink(leaf, target, node.Size, node.MtimeUnix, node.Format, true)
		if err != nil {
			return nil, err
		}
		return []ImportPayload{{Source: loc}}, nil
	}
	return blockLinkPayloads(src, target, node)
}

func blockLinkPayloads(src, target Repo, node ImageMeta) ([]ImportPayload, error) {
	if err := orderedBlocks(node); err != nil {
		return nil, err
	}
	payloads := make([]ImportPayload, len(node.Blocks))
	files := make([]BlockFile, len(node.Blocks))
	for i, block := range node.Blocks {
		leaf, err := src.ObjectBlockPath(node.ID, block.Name)
		if err != nil {
			return nil, err
		}
		loc, err := resolvedLink(leaf, target, block.Size, block.MtimeUnix, node.Format, false)
		if err != nil {
			return nil, fmt.Errorf("block %s: %w", block.Name, err)
		}
		payloads[i] = ImportPayload{Block: block.Name, Source: loc}
		files[i] = BlockFile{Path: leaf, Name: block.Name}
	}
	typ, err := ProbeLogicalFormat(files)
	if err != nil {
		return nil, err
	}
	if typ != node.Format.Type {
		return nil, fmt.Errorf("format %s does not match %s", typ, node.Format.Type)
	}
	if err := refuseLinkFormat(typ); err != nil {
		return nil, err
	}
	return payloads, nil
}

func resolvedLink(leaf string, target Repo, size, mtime int64, format Format, probe bool) (string, error) {
	resolved, err := ResolvePayloadBytesPath(leaf)
	if err != nil {
		return "", err
	}
	loc, err := canonicalPath(resolved)
	if err != nil {
		return "", err
	}
	if !probe {
		return loc, acceptLinkLeaf(target, loc, size, mtime)
	}
	if err := acceptProbedLink(target, loc, size, mtime, format); err != nil {
		return "", err
	}
	return loc, nil
}

func acceptProbedLink(repo Repo, loc string, size, mtime int64, format Format) error {
	if err := acceptLinkLeaf(repo, loc, size, mtime); err != nil {
		return err
	}
	got, err := probeImportFile(loc)
	if err != nil {
		return err
	}
	if got.Type != format.Type {
		return fmt.Errorf("format %s does not match %s", got.Type, format.Type)
	}
	return refuseLinkFormat(format.Type)
}

func acceptLinkLeaf(repo Repo, loc string, size, mtime int64) error {
	if err := validateImportLocation(loc, repo.Dir); err != nil {
		return err
	}
	info, err := os.Stat(loc)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", loc)
	}
	if info.Size() != size || info.ModTime().Unix() != mtime {
		return errors.New("source attribute conflict")
	}
	return nil
}

func writeLinkBatch(repo Repo, merge ImportMerge, cands []ImportCandidate, failID string) error {
	for i, item := range merge.Items {
		if item.Action != MergeAdd {
			continue
		}
		if cands[i].Node.ID == failID {
			return fmt.Errorf("version %s: injected", failID)
		}
		if err := writeOneLink(repo, cands[i]); err != nil {
			return err
		}
	}
	return syncDir(repo.Dir, hooks{})
}

func writeOneLink(repo Repo, cand ImportCandidate) error {
	if len(cand.Node.Blocks) == 0 {
		path, err := repo.ObjectPayloadPath(cand.Node)
		if err != nil {
			return err
		}
		return writePosition(path, cand.Payloads[0].Source)
	}
	dir, err := repo.ObjectDirPath(cand.Node.ID)
	if err != nil {
		return err
	}
	for _, payload := range cand.Payloads {
		path, err := repo.ObjectBlockPath(cand.Node.ID, payload.Block)
		if err != nil {
			return err
		}
		if err := writePosition(path, payload.Source); err != nil {
			return err
		}
	}
	return writeConcatView(dir, blockNames(cand.Node.Blocks))
}

func writePosition(path, loc string) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("payload already exists: %s", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := writeTemp(filepath.Dir(path), filepath.Base(path), []byte(loc+"\n"), hooks{})
	if err != nil {
		return err
	}
	defer removeFile(tmp)
	return (hooks{}).do(tmp, path)
}

func invalidateVersionIndexes(repo Repo) error {
	names := []string{fileSHA256Index, fileContentSHA256Index, fileVersionRevision}
	for _, name := range names {
		err := os.Remove(filepath.Join(repo.Dir, name))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return syncDir(repo.Dir, hooks{})
}
