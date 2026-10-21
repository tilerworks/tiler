package image

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ErrMetaMissing means the active squashfs has no metadata file.
var ErrMetaMissing = errors.New("meta missing")

// archiveParkSuffix marks an archive moved aside while hold is
// renamed onto its path. The suffix is not a full id.
const archiveParkSuffix = ".replacing"

// hooks are test seams. before runs immediately before each rename.
// rename replaces os.Rename when it is set. sync replaces directory
// sync when it is set.
type hooks struct {
	before func(oldpath, newpath string) error
	rename func(oldpath, newpath string) error
	sync   func(dir string) error
}

// Publish writes data and meta into the active slot.
//
// An existing active chain is renamed into hold/<old-id> before the
// new bytes become active. If activation fails, that chain is put
// back. When refs does not hold the previous chain, hold is renamed
// to archives in the same call, replacing an older archive of that
// id. A nil Refs holds nothing. The same full id is left in place
// only when both chains are empty.
//
// Files are written to a temporary name in the destination directory
// and synced. The directory is synced again after the name is linked
// and after rename. A failed rename removes the temporary file. A
// directory sync that fails after the rename removes that published
// file so the active slot is not left one-sided. A cross-device
// rename puts any moved active files back and leaves the previous
// active image intact.
//
// The next publish repairs a crash that restored only part of the
// previous chain, dropped one active file, or parked an archive
// beside its final name. Layer files left in the active incr
// directory, and blocks/<id> directories a chain lists, move into
// the one hold whose chain lists them.
// Unreferenced holds are archived even when another image is already
// active, and republishing a pinned id returns the chain layers its
// hold still owns instead of deleting them.
func (repo Repo) Publish(data []byte, meta Meta, refs Refs) error {
	return publish(repo, data, meta, refs, hooks{})
}

func publish(repo Repo, data []byte, meta Meta, refs Refs, seam hooks) error {
	if err := validatePublish(repo, data, meta); err != nil {
		return err
	}
	existing, exists, err := preparePublish(repo, refs, seam, "")
	if err != nil {
		return err
	}
	if exists && sameIdleFull(existing, meta) {
		return nil
	}
	imgTmp, metaTmp, err := stage(repo.Dir, data, meta, seam)
	if err != nil {
		return err
	}
	if err := commit(repo, existing, exists, meta, imgTmp, repo.ImagePath(), metaTmp, refs, seam); err != nil {
		removeFile(imgTmp)
		removeFile(metaTmp)
		return err
	}
	return nil
}

// publishStaged publishes a temporary image and meta pair that
// already rest synced in the repository directory. Builds hand the
// packer output over this way instead of copying the bytes through
// memory again. The temps never survive the call: renamed on
// success, deleted on the idle no-op and on every error.
func publishStaged(repo Repo, meta Meta, refs Refs, imgTmp, metaTmp string, seam hooks) error {
	defer removeFile(imgTmp)
	defer removeFile(metaTmp)
	if err := validateMeta(repo, meta); err != nil {
		return err
	}
	existing, exists, err := preparePublish(repo, refs, seam, "")
	if err != nil {
		return err
	}
	if exists && sameIdleFull(existing, meta) {
		return nil
	}
	match, err := fileMatchesMeta(imgTmp, meta)
	if err != nil {
		return err
	}
	if !match {
		return errors.New("publish: payload does not match meta")
	}
	return commit(repo, existing, exists, meta, imgTmp, repo.ImagePath(), metaTmp, refs, seam)
}

// publishStagedPayload publishes a temporary split payload directory
// and meta pair that already rest synced in the repository directory.
// It is the directory-payload sibling of publishStaged: the payload
// directory lands with one rename onto its final layout place, then
// the meta rename follows, and a failure puts the previous active
// payload back. The temps never survive the call: renamed on success,
// removed on the idle no-op and on every error.
func publishStagedPayload(
	repo Repo,
	meta Meta,
	refs Refs,
	payloadTmp, finalPayloadDir, metaTmp string,
	seam hooks,
) error {
	defer func() { _ = os.RemoveAll(payloadTmp) }()
	defer removeFile(metaTmp)
	if err := validateMeta(repo, meta); err != nil {
		return err
	}
	if PayloadKind(meta) == PayloadSingle {
		return errors.New("publish: staged payload requires a split manifest")
	}
	existing, exists, err := preparePublish(repo, refs, seam, payloadTmp)
	if err != nil {
		return err
	}
	if exists && sameIdleFull(existing, meta) {
		return nil
	}
	match, err := stagedPayloadMatches(payloadTmp, meta)
	if err != nil {
		return err
	}
	if !match {
		return errors.New("publish: payload does not match meta")
	}
	return commit(repo, existing, exists, meta, payloadTmp, finalPayloadDir, metaTmp, refs, seam)
}

// stagedPayloadMatches checks a scratch payload directory against the
// manifest that publishes with it. The scratch layout matches the
// final one: block files rest directly inside.
func stagedPayloadMatches(payloadTmp string, meta Meta) (bool, error) {
	return dirMatchesBlocks(payloadTmp, meta.Blocks)
}

// preparePublish repairs the leftovers of earlier crashes and
// archives the holds no reference pins, so the active slot is ready
// for a new image. A split publish in progress passes its own
// staging directory as keep so the repair sweep spares it.
func preparePublish(repo Repo, refs Refs, seam hooks, keep string) (Meta, bool, error) {
	if err := repairTornActive(repo, seam, keep); err != nil {
		return Meta{}, false, err
	}
	if err := repairParkedArchives(repo, seam); err != nil {
		return Meta{}, false, err
	}
	existing, exists, err := loadActive(repo)
	if err != nil {
		return Meta{}, false, err
	}
	// Return torn layers to the hold whose chain owns them and
	// archive the holds no reference pins, before the active slot is
	// reused by the new image.
	if err := archiveUnreferencedHolds(repo, refs, seam); err != nil {
		return Meta{}, false, err
	}
	return existing, exists, nil
}

func validatePublish(repo Repo, data []byte, meta Meta) error {
	if err := validateMeta(repo, meta); err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != meta.SHA256 || int64(len(data)) != meta.Size {
		return errors.New("publish: payload does not match meta")
	}
	return nil
}

func validateMeta(repo Repo, meta Meta) error {
	if meta.Kind != KindFull {
		return errors.New("publish: active meta kind must be full")
	}
	if meta.Owner != repo.Owner || meta.Repo != repo.Repo {
		return errors.New("publish: meta name does not match repository")
	}
	return meta.Validate()
}

func sameIdleFull(existing, next Meta) bool {
	return len(existing.Chain) == 0 && len(next.Chain) == 0 &&
		existing.ID == next.ID && existing.SHA256 == next.SHA256
}

// loadActive reads the active meta and checks that the payload of
// its kind is in place. The payload shape follows from the meta, so
// the meta leads: a slot whose meta rests without its payload fails
// with the missing-payload error of its form, and a payload resting
// without any meta stays ErrMetaMissing.
func loadActive(repo Repo) (Meta, bool, error) {
	meta, err := Load(repo.MetaPath())
	if errors.Is(err, os.ErrNotExist) {
		return payloadWithoutMeta(repo)
	}
	if err != nil {
		return Meta{}, false, err
	}
	there, err := activePayloadExists(repo, meta)
	if err != nil {
		return Meta{}, false, err
	}
	if !there {
		return Meta{}, false, missingPayloadError(meta)
	}
	return meta, true, nil
}

// payloadWithoutMeta handles a slot without an active meta. Any
// payload form resting there is an orphan; a truly empty slot reports
// no active image.
func payloadWithoutMeta(repo Repo) (Meta, bool, error) {
	there, err := anyPayloadExists(repo.Dir)
	if err != nil {
		return Meta{}, false, err
	}
	if there {
		return Meta{}, false, ErrMetaMissing
	}
	return Meta{}, false, nil
}

func missingPayloadError(meta Meta) error {
	if PayloadKind(meta) == PayloadSingle {
		return errors.New("publish: active image missing")
	}
	return errors.New("publish: active payload missing")
}

// singleFileNames lists the single-file payload names that count in
// a directory. dmg meta names image.dmg. Squashfs, erofs, and any
// other recorded type keep image.sqsh. An empty type means the meta
// that would name the file is missing, so both names count.
func singleFileNames(formatType string) []string {
	if formatType == "" {
		return []string{fileImage, ImageDMG}
	}
	return []string{activeImageName(formatType)}
}

// payloadFormPaths lists the payload shapes as they rest in a
// directory that preserves the active layout: the repository
// directory, a hold, or an archive. formatType selects the
// single-file name. Blocks and members stay in every list; repair
// still recognizes those leftovers.
func payloadFormPaths(dir, formatType string) []string {
	names := singleFileNames(formatType)
	paths := make([]string, 0, len(names)+2)
	for _, name := range names {
		paths = append(paths, filepath.Join(dir, name))
	}
	return append(paths,
		filepath.Join(dir, dirBlocks),
		filepath.Join(dir, dirMembers),
	)
}

// anyPayloadExists reports whether any payload form rests in a
// directory. Presence decides the shape when the meta that would
// name the kind is missing, so both single-file names count.
func anyPayloadExists(dir string) (bool, error) {
	return payloadPresent(dir, "")
}

// payloadPresent reports whether the payload forms for formatType
// rest in dir. dmg looks for image.dmg. Every other type looks for
// image.sqsh. An empty formatType accepts either single-file name.
func payloadPresent(dir, formatType string) (bool, error) {
	for _, path := range payloadFormPaths(dir, formatType) {
		there, err := pathExists(path)
		if err != nil {
			return false, err
		}
		if there {
			return true, nil
		}
	}
	return false, nil
}

// activePayloadPath is where the payload of the kind of meta rests
// in the active slot.
func activePayloadPath(repo Repo, meta Meta) (string, error) {
	if PayloadKind(meta) == PayloadBlocks {
		return repo.BlocksDirPath(meta.ID)
	}
	return filepath.Join(repo.Dir, activeImageName(meta.Format.Type)), nil
}

// payloadRoot is the outermost file or directory that carries the
// payload of the kind of meta inside a directory preserving the
// active layout. Moving the root as a whole keeps the layout
// identical on both sides: the blocks wrapper travels with its
// nested id directory. The single-file name follows the format, so
// dmg moves image.dmg and squashfs and erofs move image.sqsh.
func payloadRoot(dir string, meta Meta) string {
	if PayloadKind(meta) == PayloadBlocks {
		return filepath.Join(dir, dirBlocks)
	}
	return filepath.Join(dir, activeImageName(meta.Format.Type))
}

func activePayloadExists(repo Repo, meta Meta) (bool, error) {
	path, err := activePayloadPath(repo, meta)
	if err != nil {
		return false, err
	}
	return pathExists(path)
}

// holdPayloadPath is where the payload of the kind of meta rests
// inside a hold directory; holds preserve the active layout. The
// single-file name follows the format, so a dmg hold keeps
// image.dmg.
func holdPayloadPath(hold string, meta Meta) string {
	if PayloadKind(meta) == PayloadBlocks {
		return filepath.Join(hold, dirBlocks, meta.ID)
	}
	return filepath.Join(hold, activeImageName(meta.Format.Type))
}

func stage(dir string, data []byte, meta Meta, seam hooks) (string, string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	body, err := Encode(meta)
	if err != nil {
		return "", "", err
	}
	imgTmp, err := writeTemp(dir, fileImage, data, seam)
	if err != nil {
		return "", "", err
	}
	metaTmp, err := writeTemp(dir, fileMeta, body, seam)
	if err != nil {
		removeFile(imgTmp)
		return "", "", err
	}
	return imgTmp, metaTmp, nil
}

func writeTemp(dir, final string, data []byte, seam hooks) (string, error) {
	name, err := tempName(final)
	if err != nil {
		return "", err
	}
	tmp := filepath.Join(dir, name)
	if err := writeSynced(tmp, data); err != nil {
		removeFile(tmp)
		return "", err
	}
	if err := syncDir(dir, seam); err != nil {
		removeFile(tmp)
		return "", err
	}
	return tmp, nil
}

func tempName(final string) (string, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s%d.%s", tempPrefix(final), os.Getpid(), hex.EncodeToString(buf[:])), nil
}

// tempPrefix is the dotted prefix every tempName output starts with.
// Repair recognizes leftovers through it.
func tempPrefix(final string) string {
	return "." + final + "."
}

// isPayloadTempName reports whether name marks a scratch blocks
// payload directory.
func isPayloadTempName(name string) bool {
	return strings.HasPrefix(name, tempPrefix(blocksTempFinal))
}

func writeSynced(tmp string, data []byte) error {
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, werr := file.Write(data)
	serr := file.Sync()
	cerr := file.Close()
	return joinWriteErr(werr, serr, cerr)
}

func joinWriteErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// commit moves the existing active slot aside, renames the staged
// payload and meta into place, and settles the holds. The payload
// source is a temporary file or directory; the destination is its
// final layout place for the kind of next.
func commit(
	repo Repo,
	existing Meta,
	exists bool,
	next Meta,
	payloadSrc, payloadDst, metaTmp string,
	refs Refs,
	seam hooks,
) error {
	if exists {
		if err := moveActiveToHold(repo, existing, seam); err != nil {
			return err
		}
	}
	if err := activate(repo, payloadSrc, payloadDst, metaTmp, seam); err != nil {
		if !exists {
			return err
		}
		if undoErr := restoreHold(repo, existing.ID, seam); undoErr != nil {
			return fmt.Errorf("%w (undo: %v)", err, undoErr)
		}
		return err
	}
	// This id is active again, so a hold left from an earlier pin is
	// stale. The chain just moved aside uses the previous id.
	if !exists || existing.ID != next.ID {
		if err := retireStaleHold(repo, next.ID, next.Chain, refs, seam); err != nil {
			return err
		}
	}
	if !exists {
		return nil
	}
	return finishHold(repo, existing, refs, seam)
}

func moveActiveToHold(repo Repo, meta Meta, seam hooks) error {
	hold, err := repo.HoldPath(meta.ID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(hold), 0o755); err != nil {
		return err
	}
	// A hold left behind when this id became active again may own
	// incremental blobs the active slot lacks. Move them back before
	// dropping the stale directory so no layer is deleted while the
	// chain that lists them retires into this hold.
	if err := reclaimHoldIncr(repo, hold, seam); err != nil {
		return err
	}
	if err := reclaimHoldBlocks(repo, hold, seam); err != nil {
		return err
	}
	if err := os.RemoveAll(hold); err != nil {
		return err
	}
	if err := os.Mkdir(hold, 0o755); err != nil {
		return err
	}
	moves, err := activeMoves(repo, hold, meta)
	if err != nil {
		removeEmpty(hold)
		return err
	}
	return renameAll(hold, moves, seam)
}

// activeMoves lists the renames that carry the active slot into a
// hold. The hold preserves the active layout: the payload root meta
// names plus meta.yaml, and incr/ only for the single-file form.
// Directory payloads move as one rename each way. For blocks that
// one rename is the whole blocks/ tree, so chain layer directories
// reclaimed into the active slot travel with the full image. Do not
// MkdirAll the hold blocks/ destination first: rename onto an
// existing directory fails.
func activeMoves(repo Repo, hold string, meta Meta) ([][2]string, error) {
	moves := [][2]string{
		{payloadRoot(repo.Dir, meta), payloadRoot(hold, meta)},
		{repo.MetaPath(), filepath.Join(hold, fileMeta)},
	}
	if PayloadKind(meta) != PayloadSingle {
		return moves, nil
	}
	incr := filepath.Join(repo.Dir, dirIncr)
	info, err := os.Stat(incr)
	if errors.Is(err, os.ErrNotExist) {
		return moves, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("publish: %s is not a directory", incr)
	}
	return append(moves, [2]string{incr, filepath.Join(hold, dirIncr)}), nil
}

func renameAll(hold string, moves [][2]string, seam hooks) error {
	var done [][2]string
	for _, move := range moves {
		if err := seam.do(move[0], move[1]); err != nil {
			return restore(hold, done, err)
		}
		done = append(done, move)
	}
	return nil
}

func restore(hold string, done [][2]string, cause error) error {
	if undoErr := undoMoves(done); undoErr != nil {
		return fmt.Errorf("publish: %w (undo: %v)", cause, undoErr)
	}
	removeEmpty(filepath.Join(hold, dirBlocks))
	removeEmpty(hold)
	removeEmpty(filepath.Dir(hold))
	return fmt.Errorf("publish: %w", cause)
}

func undoMoves(done [][2]string) error {
	var errs []error
	for i := len(done) - 1; i >= 0; i-- {
		if err := os.Rename(done[i][1], done[i][0]); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// activate renames the staged payload onto its final place and then
// the staged meta. The payload parent directory is created first;
// only the blocks form nests its payload below the repository
// directory. A failure removes whatever landed so the active slot is
// never left one-sided.
func activate(repo Repo, payloadSrc, payloadDst, metaTmp string, seam hooks) error {
	if err := os.MkdirAll(filepath.Dir(payloadDst), 0o755); err != nil {
		return err
	}
	if err := seam.do(payloadSrc, payloadDst); err != nil {
		// A sync error means the rename already published the
		// payload; a rename error leaves the destination absent.
		// Clearing it is correct either way.
		removePayload(repo, payloadDst)
		return fmt.Errorf("publish: %w", err)
	}
	if err := seam.do(metaTmp, repo.MetaPath()); err != nil {
		removePayload(repo, payloadDst)
		undoLanded(metaTmp, repo.MetaPath())
		return fmt.Errorf("publish: %w", err)
	}
	return nil
}

// removePayload deletes a payload that landed in the active slot,
// file or directory, together with the wrapper directory that only
// the blocks form creates.
func removePayload(repo Repo, dst string) {
	_ = os.RemoveAll(dst)
	if parent := filepath.Dir(dst); parent != repo.Dir {
		removeEmpty(parent)
	}
}

// undoLanded removes dst when src was already renamed onto it.
func undoLanded(src, dst string) {
	if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
		removeFile(dst)
	}
}

func finishHold(repo Repo, old Meta, refs Refs, seam hooks) error {
	held, err := heldBy(refs, refFor(repo, old.ID))
	if err != nil {
		return err
	}
	if held {
		return nil
	}
	return archiveHold(repo, old.ID, seam)
}

func archiveUnreferencedHolds(repo Repo, refs Refs, seam hooks) error {
	ids, err := listHoldIDs(repo)
	if err != nil {
		return err
	}
	if err := sweepActiveIncr(repo, ids, seam); err != nil {
		return err
	}
	if err := sweepActiveBlockDirs(repo, ids, seam); err != nil {
		return err
	}
	var pending []string
	for _, id := range ids {
		held, err := heldBy(refs, refFor(repo, id))
		if err != nil {
			return err
		}
		if held {
			continue
		}
		pending = append(pending, id)
	}
	for _, id := range pending {
		if err := archiveHold(repo, id, seam); err != nil {
			return err
		}
	}
	return nil
}

// sweepActiveIncr moves active layer files into the one hold whose
// meta chain lists them. A torn replace can leave incr/ behind while
// image and meta already rest in a hold; returning the files by
// chain id keeps foreign layers out of unrelated archives and
// completes the move even when a reference pins the hold. Files the
// active meta claims, several holds claim, or no hold claims stay
// where they are.
func sweepActiveIncr(repo Repo, holdIDs []string, seam hooks) error {
	incr := filepath.Join(repo.Dir, dirIncr)
	entries, err := os.ReadDir(incr)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	active, err := activeChainIDs(repo)
	if err != nil {
		return err
	}
	claims, err := holdChainClaims(repo, holdIDs)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		id, ok := layerID(entry.Name())
		if !ok || active[id] || len(claims[id]) != 1 {
			continue
		}
		if err := moveLayerIntoHold(repo, incr, entry.Name(), claims[id][0], seam); err != nil {
			return err
		}
	}
	removeEmpty(incr)
	return nil
}

// sweepActiveBlockDirs moves blocks/<id> directories left in the
// active slot into the one hold whose chain lists that id. A crashed
// restore or return of chain layers can leave one directory behind
// after the rest of the hold moved; deleting that directory as an
// orphan payload would drop the layer. Directories the active meta
// still claims, several holds claim, or no hold claims stay where
// they are.
func sweepActiveBlockDirs(repo Repo, holdIDs []string, seam hooks) error {
	dir := filepath.Join(repo.Dir, dirBlocks)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	active, err := activeBlockOwners(repo)
	if err != nil {
		return err
	}
	claims, err := holdChainClaims(repo, holdIDs)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := sweepOneBlockDir(repo, entry, active, claims, seam); err != nil {
			return err
		}
	}
	removeEmpty(dir)
	return nil
}

func sweepOneBlockDir(repo Repo, entry os.DirEntry, active map[string]bool, claims map[string][]string, seam hooks) error {
	if !entry.IsDir() {
		return nil
	}
	id := entry.Name()
	if checkID(id) != nil || active[id] || len(claims[id]) != 1 {
		return nil
	}
	return moveBlockDirIntoHold(repo, id, claims[id][0], seam)
}

// activeBlockOwners lists the full id and chain ids of the active
// meta. A slot without meta claims nothing, which is what a torn
// restore looks like after meta.yaml has already reached the hold.
func activeBlockOwners(repo Repo) (map[string]bool, error) {
	meta, err := Load(repo.MetaPath())
	if errors.Is(err, os.ErrNotExist) {
		return map[string]bool{}, nil
	}
	if err != nil {
		return nil, err
	}
	ids := chainIDs(meta)
	if meta.ID != "" {
		ids[meta.ID] = true
	}
	return ids, nil
}

func moveBlockDirIntoHold(repo Repo, id, holdID string, seam hooks) error {
	hold, err := repo.HoldPath(holdID)
	if err != nil {
		return err
	}
	src := filepath.Join(repo.Dir, dirBlocks, id)
	dst := filepath.Join(hold, dirBlocks, id)
	return moveIfAbsent(src, dst, seam)
}

func moveLayerIntoHold(repo Repo, incr, name, holdID string, seam hooks) error {
	hold, err := repo.HoldPath(holdID)
	if err != nil {
		return err
	}
	dst := filepath.Join(hold, dirIncr, name)
	there, err := pathExists(dst)
	if err != nil || there {
		return err
	}
	if err := os.MkdirAll(filepath.Join(hold, dirIncr), 0o755); err != nil {
		return err
	}
	return seam.do(filepath.Join(incr, name), dst)
}

// activeChainIDs lists the layer ids claimed by the active meta. A
// slot without an image claims nothing.
func activeChainIDs(repo Repo) (map[string]bool, error) {
	there, err := pathExists(repo.ImagePath())
	if err != nil || !there {
		return nil, err
	}
	meta, err := Load(repo.MetaPath())
	if err != nil {
		return nil, err
	}
	return chainIDs(meta), nil
}

func chainIDs(meta Meta) map[string]bool {
	ids := make(map[string]bool, len(meta.Chain))
	for _, entry := range meta.Chain {
		ids[entry.ID] = true
	}
	return ids
}

// holdChainClaims maps every layer id to the holds whose meta chain
// lists it. A hold without a meta claims nothing.
func holdChainClaims(repo Repo, holdIDs []string) (map[string][]string, error) {
	claims := make(map[string][]string)
	for _, holdID := range holdIDs {
		hold, err := repo.HoldPath(holdID)
		if err != nil {
			return nil, err
		}
		meta, err := Load(filepath.Join(hold, fileMeta))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, entry := range meta.Chain {
			claims[entry.ID] = append(claims[entry.ID], holdID)
		}
	}
	return claims, nil
}

// layerID is the content id encoded in an incr/<id>.sqsh name.
func layerID(name string) (string, bool) {
	id, ok := strings.CutSuffix(name, squashExt)
	if !ok || checkID(id) != nil {
		return "", false
	}
	return id, true
}

func archiveHold(repo Repo, id string, seam hooks) error {
	hold, err := repo.HoldPath(id)
	if err != nil {
		return err
	}
	if _, err := os.Stat(hold); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	arch, err := repo.ArchivePath(id)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(arch), 0o755); err != nil {
		return err
	}
	parked := arch + archiveParkSuffix
	if err := restoreParked(arch, parked); err != nil {
		return err
	}
	if err := parkArchive(arch, parked, seam); err != nil {
		return err
	}
	if err := syncDir(hold, seam); err != nil {
		return undoPark(arch, parked, err)
	}
	if err := seam.do(hold, arch); err != nil {
		if _, statErr := os.Stat(hold); errors.Is(statErr, os.ErrNotExist) {
			// The rename landed and only the directory sync failed.
			if rmErr := os.RemoveAll(parked); rmErr != nil {
				return fmt.Errorf("publish: %w (undo: %v)", err, rmErr)
			}
			return fmt.Errorf("publish: %w", err)
		}
		return undoPark(arch, parked, fmt.Errorf("publish: %w", err))
	}
	if err := os.RemoveAll(parked); err != nil {
		return err
	}
	removeEmpty(filepath.Dir(hold))
	return nil
}

// parkArchive moves an existing archive aside in the same directory.
// The parked copy is put back when the replacement rename does not land.
func parkArchive(arch, parked string, seam hooks) error {
	_, err := os.Lstat(arch)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := os.Rename(arch, parked); err != nil {
		return fmt.Errorf("publish: %w", err)
	}
	if err := syncDir(filepath.Dir(arch), seam); err != nil {
		if undoErr := os.Rename(parked, arch); undoErr != nil {
			return fmt.Errorf("publish: %w (undo: %v)", err, undoErr)
		}
		return err
	}
	return nil
}

func undoPark(arch, parked string, cause error) error {
	_, err := os.Lstat(parked)
	if errors.Is(err, os.ErrNotExist) {
		return cause
	}
	if err != nil {
		return fmt.Errorf("%w (undo: %v)", cause, err)
	}
	if _, err := os.Lstat(arch); err == nil {
		return cause
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w (undo: %v)", cause, err)
	}
	if err := os.Rename(parked, arch); err != nil {
		return fmt.Errorf("%w (undo: %v)", cause, err)
	}
	return cause
}

func restoreParked(arch, parked string) error {
	_, err := os.Lstat(parked)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = os.Lstat(arch)
	if err == nil {
		return os.RemoveAll(parked)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(parked, arch)
}

func listHoldIDs(repo Repo) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(repo.Dir, dirHold))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if err := checkID(entry.Name()); err != nil {
			return nil, err
		}
		ids = append(ids, entry.Name())
	}
	return ids, nil
}

func refFor(repo Repo, id string) Ref {
	return Ref{Owner: repo.Owner, Repo: repo.Repo, FullID: id}
}

func removeHold(repo Repo, id string) error {
	hold, err := repo.HoldPath(id)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(hold); err != nil {
		return err
	}
	removeEmpty(filepath.Dir(hold))
	return nil
}

// retireStaleHold clears the hold of an id that just became active
// again. Layers the new active chain lists but that rest only in the
// hold move back into the active incr directory first, and a hold
// still pinned by refs survives until a later sweep archives it.
func retireStaleHold(repo Repo, id string, chain []ChainEntry, refs Refs, seam hooks) error {
	hold, err := repo.HoldPath(id)
	if err != nil {
		return err
	}
	there, err := pathExists(hold)
	if err != nil || !there {
		return err
	}
	if err := returnChainLayers(repo, hold, chain, seam); err != nil {
		return err
	}
	held, err := heldBy(refs, refFor(repo, id))
	if err != nil {
		return err
	}
	if held {
		return nil
	}
	return removeHold(repo, id)
}

// returnChainLayers moves the layers the chain lists from the hold
// back into the active slot. Single-file layers come from incr/;
// blocks layers come from blocks/<id>/. Files the active slot already
// has stay untouched.
func returnChainLayers(repo Repo, hold string, chain []ChainEntry, seam hooks) error {
	for _, entry := range chain {
		if err := returnOneLayer(repo, hold, entry, seam); err != nil {
			return err
		}
	}
	removeEmpty(filepath.Join(hold, dirIncr))
	removeEmpty(filepath.Join(hold, dirBlocks))
	return nil
}

func returnOneLayer(repo Repo, hold string, entry ChainEntry, seam hooks) error {
	if len(entry.Blocks) > 0 {
		dst, err := repo.BlocksDirPath(entry.ID)
		if err != nil {
			return err
		}
		src := filepath.Join(hold, dirBlocks, entry.ID)
		return moveIfAbsent(src, dst, seam)
	}
	dst, err := repo.IncrPath(entry.ID)
	if err != nil {
		return err
	}
	src := filepath.Join(hold, dirIncr, entry.ID+squashExt)
	return moveIfAbsent(src, dst, seam)
}

// moveIfAbsent renames src onto dst when dst is free and src exists.
// A missing source or an occupied destination is left as it is.
func moveIfAbsent(src, dst string, seam hooks) error {
	there, err := pathExists(dst)
	if err != nil || there {
		return err
	}
	present, err := pathExists(src)
	if err != nil || !present {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return seam.do(src, dst)
}

// reclaimHoldIncr moves the layer files a stale hold of the active id
// owns but the active slot lacks, so the retiring chain carries every
// blob back into its fresh hold instead of deleting them.
func reclaimHoldIncr(repo Repo, hold string, seam hooks) error {
	entries, err := os.ReadDir(filepath.Join(hold, dirIncr))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	dstDir := filepath.Join(repo.Dir, dirIncr)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		dst := filepath.Join(dstDir, entry.Name())
		there, err := pathExists(dst)
		if err != nil || there {
			return err
		}
		if err := os.MkdirAll(dstDir, 0o755); err != nil {
			return err
		}
		if err := seam.do(filepath.Join(hold, dirIncr, entry.Name()), dst); err != nil {
			return err
		}
	}
	return nil
}

// reclaimHoldBlocks moves blocks/<id> directories a stale hold of the
// active id owns but the active slot lacks. The following removal of
// the hold would otherwise delete chain layers that the fresh hold
// is supposed to carry.
func reclaimHoldBlocks(repo Repo, hold string, seam hooks) error {
	entries, err := os.ReadDir(filepath.Join(hold, dirBlocks))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		src := filepath.Join(hold, dirBlocks, entry.Name())
		dst := filepath.Join(repo.Dir, dirBlocks, entry.Name())
		if err := moveIfAbsent(src, dst, seam); err != nil {
			return err
		}
	}
	return nil
}

// repairTornActive finishes a crash in the middle of activate or
// restoreHold. A one-sided active payload is either completed from
// hold or removed when hold has nothing that matches it. Stray
// scratch payload directories from crashed split builds are swept
// first; keep names the staging directory of a publish in progress,
// which the sweep must spare.
func repairTornActive(repo Repo, seam hooks, keep string) error {
	if err := sweepStrayPayloadTemps(repo, seam, keep); err != nil {
		return err
	}
	metaExists, err := pathExists(repo.MetaPath())
	if err != nil {
		return err
	}
	// The single-file name follows the meta. A dmg record plus
	// image.dmg is a live payload; image.sqsh is not that file.
	// With no meta, either single-file name is still a payload.
	formatType := ""
	if metaExists {
		meta, err := Load(repo.MetaPath())
		if err != nil {
			return err
		}
		formatType = meta.Format.Type
	}
	payloadExists, err := payloadPresent(repo.Dir, formatType)
	if err != nil {
		return err
	}
	switch {
	case !payloadExists && metaExists:
		return repairMissingImage(repo, seam)
	case payloadExists && !metaExists:
		return repairMissingMeta(repo, seam)
	case payloadExists && metaExists:
		return repairMismatchedMeta(repo, seam)
	default:
		return nil
	}
}

// sweepStrayPayloadTemps deletes scratch payload directories a
// crashed split build left in the repository directory. They follow
// the tempName pattern, so published layout directories never match.
// The keep path, when set, is the staging directory of the publish
// in progress and survives the sweep.
func sweepStrayPayloadTemps(repo Repo, seam hooks, keep string) error {
	entries, err := os.ReadDir(repo.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	changed := false
	for _, entry := range entries {
		if !isPayloadTempName(entry.Name()) {
			continue
		}
		path := filepath.Join(repo.Dir, entry.Name())
		if path == keep {
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			return err
		}
		changed = true
	}
	if !changed {
		return nil
	}
	return syncDir(repo.Dir, seam)
}

func repairMissingImage(repo Repo, seam hooks) error {
	meta, err := Load(repo.MetaPath())
	if err != nil {
		return err
	}
	hold, err := repo.HoldPath(meta.ID)
	if err != nil {
		return err
	}
	heldPayload, err := pathExists(holdPayloadPath(hold, meta))
	if err != nil {
		return err
	}
	if heldPayload {
		return restoreHold(repo, meta.ID, seam)
	}
	// The meta was published and its payload was deleted. Nothing in
	// hold can supply that payload, so drop the meta and publish
	// again.
	if err := os.Remove(repo.MetaPath()); err != nil {
		return err
	}
	return syncDir(repo.Dir, seam)
}

func repairMissingMeta(repo Repo, seam hooks) error {
	ids, err := listHoldIDs(repo)
	if err != nil {
		return err
	}
	// Salvage chain layer directories before the orphan-payload
	// cleanup. A blocks/<id> left beside a missing meta looks like a
	// payload and would be deleted with it.
	if err := sweepActiveBlockDirs(repo, ids, seam); err != nil {
		return err
	}
	id, ok, err := matchingPartialHold(repo)
	if err != nil {
		return err
	}
	if ok {
		return restoreHold(repo, id, seam)
	}
	if err := removeActivePayload(repo.Dir); err != nil {
		return err
	}
	return syncDir(repo.Dir, seam)
}

// removeActivePayload deletes every payload shape resting in a
// directory that preserves the active layout. The meta that would
// name the kind is missing in the repair paths, so presence decides.
func removeActivePayload(dir string) error {
	for _, path := range payloadFormPaths(dir, "") {
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	return nil
}

func repairMismatchedMeta(repo Repo, seam hooks) error {
	partial, err := partialHoldIDs(repo)
	if err != nil {
		return err
	}
	active, err := Load(repo.MetaPath())
	if err != nil {
		return err
	}
	if len(partial) == 0 {
		return restoreLeftoverIncr(repo, active.ID)
	}
	match, err := payloadMatchesMeta(repo, active)
	if err != nil {
		return err
	}
	if match {
		return restoreLeftoverIncr(repo, active.ID)
	}
	id, ok, err := matchingPartialHold(repo)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("publish: active payload does not match meta")
	}
	return restoreHold(repo, id, seam)
}

func partialHoldIDs(repo Repo) ([]string, error) {
	ids, err := listHoldIDs(repo)
	if err != nil {
		return nil, err
	}
	var partial []string
	for _, id := range ids {
		hold, err := repo.HoldPath(id)
		if err != nil {
			return nil, err
		}
		metaPath := filepath.Join(hold, fileMeta)
		meta, loadErr := Load(metaPath)
		if loadErr != nil && !errors.Is(loadErr, os.ErrNotExist) {
			return nil, loadErr
		}
		formatType := ""
		if loadErr == nil {
			formatType = meta.Format.Type
		}
		payloadThere, err := payloadPresent(hold, formatType)
		if err != nil {
			return nil, err
		}
		if payloadThere || loadErr != nil {
			continue
		}
		partial = append(partial, id)
	}
	return partial, nil
}

// matchingPartialHold is the hold whose meta matches the payload
// resting in the active slot and whose own payload was already moved
// back. More than one match is an error.
func matchingPartialHold(repo Repo) (string, bool, error) {
	partial, err := partialHoldIDs(repo)
	if err != nil {
		return "", false, err
	}
	var matched []string
	for _, id := range partial {
		hold, err := repo.HoldPath(id)
		if err != nil {
			return "", false, err
		}
		meta, err := Load(filepath.Join(hold, fileMeta))
		if err != nil {
			return "", false, err
		}
		ok, err := payloadMatchesMeta(repo, meta)
		if err != nil {
			return "", false, err
		}
		if ok {
			matched = append(matched, id)
		}
	}
	switch len(matched) {
	case 0:
		if len(partial) > 0 {
			return "", false, errors.New("publish: held meta does not match the active payload")
		}
		return "", false, nil
	case 1:
		return matched[0], true, nil
	default:
		return "", false, errors.New("publish: multiple holds match the active payload")
	}
}

func restoreLeftoverIncr(repo Repo, id string) error {
	hold, err := repo.HoldPath(id)
	if err != nil {
		return err
	}
	name, err := restoreSingleName(repo, hold)
	if err != nil {
		return err
	}
	imageThere, err := pathExists(filepath.Join(hold, name))
	if err != nil || imageThere {
		return err
	}
	metaThere, err := pathExists(filepath.Join(hold, fileMeta))
	if err != nil || metaThere {
		return err
	}
	src := filepath.Join(hold, dirIncr)
	incrThere, err := pathExists(src)
	if err != nil || !incrThere {
		return err
	}
	dst := filepath.Join(repo.Dir, dirIncr)
	dstThere, err := pathExists(dst)
	if err != nil || dstThere {
		return err
	}
	if err := os.Rename(src, dst); err != nil {
		return err
	}
	removeEmpty(hold)
	removeEmpty(filepath.Dir(hold))
	return nil
}

func repairParkedArchives(repo Repo, seam hooks) error {
	dir := filepath.Join(repo.Dir, dirArchives)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	changed := false
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, archiveParkSuffix) {
			continue
		}
		id := strings.TrimSuffix(name, archiveParkSuffix)
		if err := checkID(id); err != nil {
			continue
		}
		arch := filepath.Join(dir, id)
		if err := restoreParked(arch, filepath.Join(dir, name)); err != nil {
			return err
		}
		changed = true
	}
	if !changed {
		return nil
	}
	return syncDir(dir, seam)
}

func pathExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

// payloadMatchesMeta reports whether the payload resting in the
// active slot matches meta: the whole-file digest for the single
// form, or per-block digests for blocks. The single file is
// image.dmg when the format is dmg, and image.sqsh for squashfs and
// erofs. A payload that is absent or incomplete is a mismatch rather
// than an error, so repair keeps going; only genuine IO failures
// propagate.
func payloadMatchesMeta(repo Repo, meta Meta) (bool, error) {
	if PayloadKind(meta) == PayloadBlocks {
		dir, err := repo.BlocksDirPath(meta.ID)
		if err != nil {
			return false, err
		}
		return dirMatchesBlocks(dir, meta.Blocks)
	}
	path, err := activePayloadPath(repo, meta)
	if err != nil {
		return false, err
	}
	there, err := pathExists(path)
	if err != nil || !there {
		return false, err
	}
	return fileMatchesMeta(path, meta)
}

// dirMatchesBlocks re-hashes every manifest block under dir. The
// staged payload of a build and the published blocks directory share
// the same flat layout.
func dirMatchesBlocks(dir string, blocks []BlockEntry) (bool, error) {
	there, err := pathExists(dir)
	if err != nil || !there {
		return false, err
	}
	for _, entry := range blocks {
		if !isBlockName(entry.Name) {
			return false, nil
		}
		ok, err := fileMatches(filepath.Join(dir, entry.Name), entry.Size, entry.SHA256)
		if err != nil || !ok {
			return false, err
		}
	}
	return true, nil
}

func fileMatchesMeta(path string, meta Meta) (bool, error) {
	return fileMatches(path, meta.Size, meta.SHA256)
}

// fileMatches streams a file through sha256 and compares the digest
// and the size. A missing file is a mismatch, not an error.
func fileMatches(path string, size int64, want string) (bool, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	sum := sha256.New()
	n, copyErr := io.Copy(sum, file)
	closeErr := file.Close()
	if err := joinWriteErr(copyErr, closeErr); err != nil {
		return false, err
	}
	got := hex.EncodeToString(sum.Sum(nil))
	return got == want && n == size, nil
}

// restoreSingleName is the single-file payload a restore moves. The
// hold meta names the file when it is present; otherwise the active
// meta does. dmg uses image.dmg. Squashfs, erofs, and a restore with
// no readable meta keep image.sqsh.
func restoreSingleName(repo Repo, hold string) (string, error) {
	paths := []string{
		filepath.Join(hold, fileMeta),
		repo.MetaPath(),
	}
	for _, path := range paths {
		meta, err := Load(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		return activeImageName(meta.Format.Type), nil
	}
	return fileImage, nil
}

// restoreHold moves a hold back onto the active slot. The hold
// preserves the active layout, so every payload shape it owns moves
// back symmetrically: the single file tolerates a missing source,
// and a directory shape moves only when it rests in the hold and the
// active slot lacks it. The single-file name follows the meta, so a
// dmg hold returns image.dmg and a squashfs or erofs hold returns
// image.sqsh.
func restoreHold(repo Repo, id string, seam hooks) error {
	hold, err := repo.HoldPath(id)
	if err != nil {
		return err
	}
	if _, err := os.Stat(hold); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	name, err := restoreSingleName(repo, hold)
	if err != nil {
		return err
	}
	moves := [][2]string{
		{filepath.Join(hold, name), filepath.Join(repo.Dir, name)},
		{filepath.Join(hold, fileMeta), repo.MetaPath()},
	}
	moves, err = appendBlocksRestore(moves, hold, repo.Dir)
	if err != nil {
		return err
	}
	for _, pair := range [][2]string{
		{filepath.Join(hold, dirMembers), filepath.Join(repo.Dir, dirMembers)},
		{filepath.Join(hold, dirIncr), filepath.Join(repo.Dir, dirIncr)},
	} {
		if moves, err = appendPresentDirMove(moves, pair[0], pair[1]); err != nil {
			return err
		}
	}
	for _, move := range moves {
		if err := moveBack(move[0], move[1]); err != nil {
			return err
		}
	}
	if err := syncDir(repo.Dir, seam); err != nil {
		return err
	}
	if err := syncDir(filepath.Dir(hold), seam); err != nil {
		return err
	}
	removeEmpty(filepath.Join(hold, dirBlocks))
	removeEmpty(hold)
	removeEmpty(filepath.Dir(hold))
	return nil
}

// appendBlocksRestore adds the blocks directory move. When the active
// slot already has blocks/, each held layer directory is merged in
// instead, so a chain dir that did not travel with the full image
// still returns.
func appendBlocksRestore(moves [][2]string, hold, repoDir string) ([][2]string, error) {
	src := filepath.Join(hold, dirBlocks)
	dst := filepath.Join(repoDir, dirBlocks)
	if _, err := os.Stat(src); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return moves, nil
		}
		return nil, err
	}
	if _, err := os.Stat(dst); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return append(moves, [2]string{src, dst}), nil
		}
		return nil, err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		childSrc := filepath.Join(src, entry.Name())
		childDst := filepath.Join(dst, entry.Name())
		moves, err = appendPresentDirMove(moves, childSrc, childDst)
		if err != nil {
			return nil, err
		}
	}
	return moves, nil
}

// appendPresentDirMove adds a directory move when the source exists
// and the destination is free, the rule the incr restore always
// followed.
func appendPresentDirMove(moves [][2]string, src, dst string) ([][2]string, error) {
	if _, err := os.Stat(src); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return moves, nil
		}
		return nil, err
	}
	if _, err := os.Stat(dst); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return append(moves, [2]string{src, dst}), nil
		}
		return nil, err
	}
	return moves, nil
}

func moveBack(src, dst string) error {
	info, err := os.Stat(src)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		if _, dstErr := os.Stat(dst); dstErr == nil {
			if err := os.Remove(dst); err != nil {
				return err
			}
		} else if !errors.Is(dstErr, os.ErrNotExist) {
			return dstErr
		}
	}
	return os.Rename(src, dst)
}

func heldBy(refs Refs, ref Ref) (bool, error) {
	if refs == nil {
		return false, nil
	}
	return refs.Holds(ref)
}

// do renames oldpath to newpath and syncs the directories. The rename
// has landed when a later sync returns an error; activate and
// archiveHold undo that name if the publish cannot continue.
func (seam hooks) do(oldpath, newpath string) error {
	if seam.before != nil {
		if err := seam.before(oldpath, newpath); err != nil {
			return err
		}
	}
	var err error
	if seam.rename != nil {
		err = seam.rename(oldpath, newpath)
	} else {
		err = os.Rename(oldpath, newpath)
	}
	if err != nil {
		return err
	}
	if err := syncDir(filepath.Dir(newpath), seam); err != nil {
		return err
	}
	srcDir := filepath.Dir(oldpath)
	dstDir := filepath.Dir(newpath)
	if srcDir == dstDir {
		return nil
	}
	return syncDir(srcDir, seam)
}

func syncDir(dir string, seam hooks) error {
	if seam.sync != nil {
		return seam.sync(dir)
	}
	file, err := os.Open(dir)
	if err != nil {
		return err
	}
	serr := file.Sync()
	cerr := file.Close()
	return joinWriteErr(serr, cerr)
}

func removeFile(name string) {
	if name == "" {
		return
	}
	_ = os.Remove(name)
}

func removeEmpty(name string) {
	_ = os.Remove(name)
}
