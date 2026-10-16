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
// directory move into the one hold whose chain lists them.
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
	if err := repairTornActive(repo, seam); err != nil {
		return err
	}
	if err := repairParkedArchives(repo, seam); err != nil {
		return err
	}
	existing, exists, err := loadActive(repo)
	if err != nil {
		return err
	}
	// Return torn layers to the hold whose chain owns them and
	// archive the holds no reference pins, before the active slot is
	// reused by the new image.
	if err := archiveUnreferencedHolds(repo, refs, seam); err != nil {
		return err
	}
	if exists && sameIdleFull(existing, meta) {
		return nil
	}
	imgTmp, metaTmp, err := stage(repo.Dir, data, meta, seam)
	if err != nil {
		return err
	}
	if err := commit(repo, existing, exists, meta, imgTmp, metaTmp, refs, seam); err != nil {
		removeFile(imgTmp)
		removeFile(metaTmp)
		return err
	}
	return nil
}

func validatePublish(repo Repo, data []byte, meta Meta) error {
	if meta.Kind != KindFull {
		return errors.New("publish: active meta kind must be full")
	}
	if meta.Owner != repo.Owner || meta.Repo != repo.Repo {
		return errors.New("publish: meta name does not match repository")
	}
	if err := meta.Validate(); err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != meta.SHA256 || int64(len(data)) != meta.Size {
		return errors.New("publish: payload does not match meta")
	}
	return nil
}

func sameIdleFull(existing, next Meta) bool {
	return len(existing.Chain) == 0 && len(next.Chain) == 0 &&
		existing.ID == next.ID && existing.SHA256 == next.SHA256
}

func loadActive(repo Repo) (Meta, bool, error) {
	_, err := os.Stat(repo.ImagePath())
	if errors.Is(err, os.ErrNotExist) {
		return missingImage(repo)
	}
	if err != nil {
		return Meta{}, false, err
	}
	meta, err := Load(repo.MetaPath())
	if errors.Is(err, os.ErrNotExist) {
		return Meta{}, false, ErrMetaMissing
	}
	if err != nil {
		return Meta{}, false, err
	}
	return meta, true, nil
}

func missingImage(repo Repo) (Meta, bool, error) {
	_, err := os.Stat(repo.MetaPath())
	if errors.Is(err, os.ErrNotExist) {
		return Meta{}, false, nil
	}
	if err != nil {
		return Meta{}, false, err
	}
	return Meta{}, false, errors.New("publish: active image missing")
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
	return fmt.Sprintf(".%s.%d.%s", final, os.Getpid(), hex.EncodeToString(buf[:])), nil
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

func commit(
	repo Repo,
	existing Meta,
	exists bool,
	next Meta,
	imgTmp, metaTmp string,
	refs Refs,
	seam hooks,
) error {
	if exists {
		if err := moveActiveToHold(repo, existing.ID, seam); err != nil {
			return err
		}
	}
	if err := activate(repo, imgTmp, metaTmp, seam); err != nil {
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

func moveActiveToHold(repo Repo, id string, seam hooks) error {
	hold, err := repo.HoldPath(id)
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
	if err := os.RemoveAll(hold); err != nil {
		return err
	}
	if err := os.Mkdir(hold, 0o755); err != nil {
		return err
	}
	moves, err := activeMoves(repo, hold)
	if err != nil {
		removeEmpty(hold)
		return err
	}
	return renameAll(hold, moves, seam)
}

func activeMoves(repo Repo, hold string) ([][2]string, error) {
	moves := [][2]string{
		{repo.ImagePath(), filepath.Join(hold, fileImage)},
		{repo.MetaPath(), filepath.Join(hold, fileMeta)},
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

func activate(repo Repo, imgTmp, metaTmp string, seam hooks) error {
	if err := seam.do(imgTmp, repo.ImagePath()); err != nil {
		// A sync error means the rename already published the file.
		undoLanded(imgTmp, repo.ImagePath())
		return fmt.Errorf("publish: %w", err)
	}
	if err := seam.do(metaTmp, repo.MetaPath()); err != nil {
		removeFile(repo.ImagePath())
		undoLanded(metaTmp, repo.MetaPath())
		return fmt.Errorf("publish: %w", err)
	}
	return nil
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

// returnChainLayers moves the layer files the chain lists from the
// hold back into the active incr directory. Files the active slot
// already has stay untouched.
func returnChainLayers(repo Repo, hold string, chain []ChainEntry, seam hooks) error {
	for _, entry := range chain {
		dst, err := repo.IncrPath(entry.ID)
		if err != nil {
			return err
		}
		there, err := pathExists(dst)
		if err != nil || there {
			return err
		}
		src := filepath.Join(hold, dirIncr, entry.ID+squashExt)
		inHold, err := pathExists(src)
		if err != nil || !inHold {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := seam.do(src, dst); err != nil {
			return err
		}
	}
	removeEmpty(filepath.Join(hold, dirIncr))
	return nil
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

// repairTornActive finishes a crash in the middle of activate or
// restoreHold. A one-sided active file is either completed from hold
// or removed when hold has nothing that matches it.
func repairTornActive(repo Repo, seam hooks) error {
	imgExists, err := pathExists(repo.ImagePath())
	if err != nil {
		return err
	}
	metaExists, err := pathExists(repo.MetaPath())
	if err != nil {
		return err
	}
	switch {
	case !imgExists && metaExists:
		return repairMissingImage(repo, seam)
	case imgExists && !metaExists:
		return repairMissingMeta(repo, seam)
	case imgExists && metaExists:
		return repairMismatchedMeta(repo, seam)
	default:
		return nil
	}
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
	heldImage, err := pathExists(filepath.Join(hold, fileImage))
	if err != nil {
		return err
	}
	if heldImage {
		return restoreHold(repo, meta.ID, seam)
	}
	// The meta was published and its image was deleted. Nothing in
	// hold can supply that image, so drop the meta and publish again.
	if err := os.Remove(repo.MetaPath()); err != nil {
		return err
	}
	return syncDir(repo.Dir, seam)
}

func repairMissingMeta(repo Repo, seam hooks) error {
	id, ok, err := matchingPartialHold(repo, repo.ImagePath())
	if err != nil {
		return err
	}
	if ok {
		return restoreHold(repo, id, seam)
	}
	if err := os.Remove(repo.ImagePath()); err != nil {
		return err
	}
	return syncDir(repo.Dir, seam)
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
	match, err := fileMatchesMeta(repo.ImagePath(), active)
	if err != nil {
		return err
	}
	if match {
		return restoreLeftoverIncr(repo, active.ID)
	}
	id, ok, err := matchingPartialHold(repo, repo.ImagePath())
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("publish: active image does not match meta")
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
		imageThere, err := pathExists(filepath.Join(hold, fileImage))
		if err != nil {
			return nil, err
		}
		if imageThere {
			continue
		}
		metaThere, err := pathExists(filepath.Join(hold, fileMeta))
		if err != nil {
			return nil, err
		}
		if metaThere {
			partial = append(partial, id)
		}
	}
	return partial, nil
}

// matchingPartialHold is the hold whose meta matches imagePath and
// whose image was already moved back. More than one match is an error.
func matchingPartialHold(repo Repo, imagePath string) (string, bool, error) {
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
		ok, err := fileMatchesMeta(imagePath, meta)
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
			return "", false, errors.New("publish: held meta does not match active image")
		}
		return "", false, nil
	case 1:
		return matched[0], true, nil
	default:
		return "", false, errors.New("publish: multiple holds match the active image")
	}
}

func restoreLeftoverIncr(repo Repo, id string) error {
	hold, err := repo.HoldPath(id)
	if err != nil {
		return err
	}
	imageThere, err := pathExists(filepath.Join(hold, fileImage))
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

func fileMatchesMeta(path string, meta Meta) (bool, error) {
	file, err := os.Open(path)
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
	return got == meta.SHA256 && n == meta.Size, nil
}

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
	moves := [][2]string{
		{filepath.Join(hold, fileImage), repo.ImagePath()},
		{filepath.Join(hold, fileMeta), repo.MetaPath()},
	}
	incrHold := filepath.Join(hold, dirIncr)
	incrActive := filepath.Join(repo.Dir, dirIncr)
	if _, err := os.Stat(incrHold); err == nil {
		if _, dstErr := os.Stat(incrActive); errors.Is(dstErr, os.ErrNotExist) {
			moves = append(moves, [2]string{incrHold, incrActive})
		} else if dstErr != nil {
			return dstErr
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
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
	removeEmpty(hold)
	removeEmpty(filepath.Dir(hold))
	return nil
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
