package image

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestPublishFreshMatchesDigest(t *testing.T) {
	repo := testRepo(t)
	data := []byte("full-image-bytes")
	meta := metaFor(data)
	var activated bool
	err := publish(repo, data, meta, nil, hooks{
		before: func(oldpath, newpath string) error {
			if filepath.Base(newpath) != fileImage || filepath.Dir(newpath) != repo.Dir {
				return nil
			}
			if filepath.Dir(oldpath) != repo.Dir {
				t.Errorf("temp %s and final %s are in different directories", oldpath, newpath)
			}
			activated = true
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !activated {
		t.Fatal("image was not renamed into the active slot")
	}
	got, err := os.ReadFile(repo.ImagePath())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("active bytes = %q", got)
	}
	loaded, err := Load(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(got)
	if loaded.SHA256 != hex.EncodeToString(sum[:]) || loaded.ID != ID(sum) {
		t.Fatalf("meta digest = %s %s", loaded.ID, loaded.SHA256)
	}
	assertNoTemps(t, repo.Dir)
}

func TestPublishAbortsBeforeRename(t *testing.T) {
	repo := testRepo(t)
	data := []byte("never-active")
	err := publish(repo, data, metaFor(data), nil, hooks{
		before: func(string, string) error {
			return errors.New("cancelled before rename")
		},
	})
	if err == nil {
		t.Fatal("expected cancel")
	}
	if _, err := os.Stat(repo.ImagePath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("active image err = %v", err)
	}
	if _, err := os.Stat(repo.MetaPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("active meta err = %v", err)
	}
	assertNoTemps(t, repo.Dir)
}

func TestPublishReplaceArchivesWhenUnreferenced(t *testing.T) {
	repo := testRepo(t)
	oldData := []byte("previous-full")
	old := seedActive(t, repo, oldData, true)
	nextData := []byte("replacement-full")
	next := metaFor(nextData)
	var heldBeforeActive bool
	err := publish(repo, nextData, next, nil, hooks{
		before: func(_, newpath string) error {
			if filepath.Base(newpath) != fileImage || filepath.Dir(newpath) != repo.Dir {
				return nil
			}
			hold, holdErr := repo.HoldPath(old.ID)
			if holdErr != nil {
				t.Fatal(holdErr)
			}
			if _, statErr := os.Stat(filepath.Join(hold, fileImage)); statErr == nil {
				heldBeforeActive = true
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !heldBeforeActive {
		t.Fatal("new active image appeared before hold")
	}
	assertBytes(t, repo.ImagePath(), nextData)
	arch, err := repo.ArchivePath(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, filepath.Join(arch, fileImage), oldData)
	incr, err := chainIncr(arch, old.Chain[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, incr, []byte("layer"))
	loaded, err := Load(filepath.Join(arch, fileMeta))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != old.ID || len(loaded.Chain) != 1 {
		t.Fatalf("archived meta = %+v", loaded)
	}
	if _, err := repo.HoldPath(old.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo.Dir, dirHold, old.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("hold still present: %v", err)
	}
	assertNoTemps(t, repo.Dir)
}

func TestPublishReplaceLeavesHoldWhenReferenced(t *testing.T) {
	repo := testRepo(t)
	oldData := []byte("held-full")
	old := seedActive(t, repo, oldData, true)
	nextData := []byte("new-full")
	refs := holdSet{ids: map[string]bool{old.ID: true}}
	if err := repo.Publish(nextData, metaFor(nextData), refs); err != nil {
		t.Fatal(err)
	}
	assertBytes(t, repo.ImagePath(), nextData)
	hold, err := repo.HoldPath(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, filepath.Join(hold, fileImage), oldData)
	incr, err := chainIncr(hold, old.Chain[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, incr, []byte("layer"))
	if _, err := os.Stat(filepath.Join(repo.Dir, dirArchives, old.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("archive created while referenced: %v", err)
	}
	assertNoTemps(t, repo.Dir)
}

func TestPublishEXDEVKeepsActive(t *testing.T) {
	repo := testRepo(t)
	oldData := []byte("stable-full")
	old := seedActive(t, repo, oldData, true)
	var n int
	err := publish(repo, []byte("cross-device"), metaFor([]byte("cross-device")), nil, hooks{
		rename: func(oldpath, newpath string) error {
			n++
			if n == 2 {
				return syscall.EXDEV
			}
			return os.Rename(oldpath, newpath)
		},
	})
	if !errors.Is(err, syscall.EXDEV) {
		t.Fatalf("err = %v", err)
	}
	assertBytes(t, repo.ImagePath(), oldData)
	loaded, err := Load(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != old.ID || len(loaded.Chain) != 1 {
		t.Fatalf("active meta = %+v", loaded)
	}
	incr, err := repo.IncrPath(old.Chain[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, incr, []byte("layer"))
	if _, err := os.Stat(filepath.Join(repo.Dir, dirHold)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("hold left behind: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo.Dir, dirArchives)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("archive left behind: %v", err)
	}
	assertNoTemps(t, repo.Dir)
}

func TestPublishMetaRenameDropsPartialActive(t *testing.T) {
	repo := testRepo(t)
	data := []byte("partial-full")
	err := publish(repo, data, metaFor(data), nil, hooks{
		rename: func(oldpath, newpath string) error {
			if filepath.Base(newpath) == fileMeta && filepath.Dir(newpath) == repo.Dir {
				return errors.New("meta rename failed")
			}
			return os.Rename(oldpath, newpath)
		},
	})
	if err == nil {
		t.Fatal("expected meta rename failure")
	}
	if _, statErr := os.Stat(repo.ImagePath()); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("partial image err = %v", statErr)
	}
	if _, statErr := os.Stat(repo.MetaPath()); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("partial meta err = %v", statErr)
	}
	assertNoTemps(t, repo.Dir)
}

func TestPublishSameBytesWithEmptyChainIsIdle(t *testing.T) {
	repo := testRepo(t)
	data := []byte("same-bytes")
	meta := metaFor(data)
	if err := repo.Publish(data, meta, nil); err != nil {
		t.Fatal(err)
	}
	err := publish(repo, data, meta, nil, hooks{
		before: func(string, string) error {
			t.Fatal("idle publish renamed a file")
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo.Dir, dirArchives)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("idle publish archived: %v", err)
	}
}

func TestPublishMetaRenameRestoresPreviousActive(t *testing.T) {
	repo := testRepo(t)
	oldData := []byte("kept-full")
	old := seedActive(t, repo, oldData, true)
	err := publish(repo, []byte("incoming"), metaFor([]byte("incoming")), nil, hooks{
		rename: func(oldpath, newpath string) error {
			if filepath.Base(newpath) == fileMeta && filepath.Dir(newpath) == repo.Dir {
				return errors.New("meta rename failed")
			}
			return os.Rename(oldpath, newpath)
		},
	})
	if err == nil {
		t.Fatal("expected meta rename failure")
	}
	assertBytes(t, repo.ImagePath(), oldData)
	loaded, err := Load(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != old.ID || len(loaded.Chain) != 1 {
		t.Fatalf("active meta = %+v", loaded)
	}
	incr, err := repo.IncrPath(old.Chain[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, incr, []byte("layer"))
	if _, statErr := os.Stat(filepath.Join(repo.Dir, dirHold, old.ID)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("hold still present: %v", statErr)
	}
	next := []byte("incoming")
	if err := repo.Publish(next, metaFor(next), nil); err != nil {
		t.Fatal(err)
	}
	arch, err := repo.ArchivePath(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, filepath.Join(arch, fileImage), oldData)
	if _, statErr := os.Stat(filepath.Join(repo.Dir, dirHold, old.ID)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("retry left hold behind: %v", statErr)
	}
	assertBytes(t, repo.ImagePath(), next)
}

func TestPublishRepairsImageLeftInHold(t *testing.T) {
	repo := testRepo(t)
	oldData := []byte("torn-full")
	old := seedActive(t, repo, oldData, true)
	hold, err := repo.HoldPath(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(hold, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(repo.ImagePath(), filepath.Join(hold, fileImage)); err != nil {
		t.Fatal(err)
	}
	next := []byte("after-repair")
	if err := repo.Publish(next, metaFor(next), nil); err != nil {
		t.Fatal(err)
	}
	assertBytes(t, repo.ImagePath(), next)
	arch, err := repo.ArchivePath(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, filepath.Join(arch, fileImage), oldData)
	archivedIncr, err := chainIncr(arch, old.Chain[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, archivedIncr, []byte("layer"))
	if _, statErr := os.Stat(hold); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("hold still present: %v", statErr)
	}
}

func TestPublishArchivesOrphanHold(t *testing.T) {
	repo := testRepo(t)
	oldData := []byte("orphaned-full")
	old := seedActive(t, repo, oldData, true)
	hold, err := repo.HoldPath(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(hold, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{fileImage, fileMeta, dirIncr} {
		if err := os.Rename(filepath.Join(repo.Dir, name), filepath.Join(hold, name)); err != nil {
			t.Fatal(err)
		}
	}
	next := []byte("published-after-orphan")
	if err := repo.Publish(next, metaFor(next), nil); err != nil {
		t.Fatal(err)
	}
	assertBytes(t, repo.ImagePath(), next)
	arch, err := repo.ArchivePath(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, filepath.Join(arch, fileImage), oldData)
	archivedIncr, err := chainIncr(arch, old.Chain[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, archivedIncr, []byte("layer"))
	if _, statErr := os.Stat(filepath.Join(repo.Dir, dirHold)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("hold still present: %v", statErr)
	}
}

func TestPublishReplacesOccupiedArchive(t *testing.T) {
	repo := testRepo(t)
	oldData := []byte("previous-full")
	old := seedActive(t, repo, oldData, true)
	arch, err := repo.ArchivePath(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(arch, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(arch, fileImage), []byte("older-archive"), 0o644); err != nil {
		t.Fatal(err)
	}
	next := []byte("replacement-full")
	if err := repo.Publish(next, metaFor(next), nil); err != nil {
		t.Fatal(err)
	}
	assertBytes(t, repo.ImagePath(), next)
	assertBytes(t, filepath.Join(arch, fileImage), oldData)
	if _, statErr := os.Stat(filepath.Join(repo.Dir, dirHold, old.ID)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("hold still present: %v", statErr)
	}
}

func TestPublishIdleFinishesUnreferencedHold(t *testing.T) {
	repo := testRepo(t)
	oldData := []byte("left-in-hold")
	old := seedActive(t, repo, oldData, false)
	hold, err := repo.HoldPath(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(hold, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(repo.ImagePath(), filepath.Join(hold, fileImage)); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(repo.MetaPath(), filepath.Join(hold, fileMeta)); err != nil {
		t.Fatal(err)
	}
	next := []byte("active-now")
	nextMeta := metaFor(next)
	if err := os.WriteFile(repo.ImagePath(), next, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Store(repo.MetaPath(), nextMeta); err != nil {
		t.Fatal(err)
	}
	arch, err := repo.ArchivePath(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(arch, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(arch, fileImage), []byte("stale-archive"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := repo.Publish(next, nextMeta, nil); err != nil {
		t.Fatal(err)
	}
	assertBytes(t, filepath.Join(arch, fileImage), oldData)
	assertBytes(t, repo.ImagePath(), next)
	if _, statErr := os.Stat(hold); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("hold still present: %v", statErr)
	}
}

func TestPublishClearsHoldWhenIDBecomesActive(t *testing.T) {
	repo := testRepo(t)
	first := []byte("image-a")
	second := []byte("image-b")
	third := []byte("image-c")
	metaA := metaFor(first)
	if err := repo.Publish(first, metaA, nil); err != nil {
		t.Fatal(err)
	}
	refs := holdSet{ids: map[string]bool{metaA.ID: true}}
	if err := repo.Publish(second, metaFor(second), refs); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo.Dir, dirHold, metaA.ID)); err != nil {
		t.Fatal(err)
	}
	if err := repo.Publish(first, metaA, nil); err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(filepath.Join(repo.Dir, dirHold, metaA.ID)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("hold remained after id became active: %v", statErr)
	}
	if err := repo.Publish(third, metaFor(third), nil); err != nil {
		t.Fatal(err)
	}
	assertBytes(t, repo.ImagePath(), third)
}

func TestPublishIdleKeepsIncomingChain(t *testing.T) {
	repo := testRepo(t)
	data := []byte("same-bytes")
	meta := metaFor(data)
	if err := repo.Publish(data, meta, nil); err != nil {
		t.Fatal(err)
	}
	const layer = "fedcba9876543210"
	meta.Chain = []ChainEntry{validEntry(meta.ID, layer)}
	if err := repo.Publish(data, meta, nil); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Chain) != 1 || loaded.Chain[0].ID != layer {
		t.Fatalf("active meta = %+v", loaded)
	}
}

func TestPublishSyncsDirectoryAroundRename(t *testing.T) {
	repo := testRepo(t)
	var syncedBefore, syncedAfter, renamed bool
	data := []byte("synced-full")
	err := publish(repo, data, metaFor(data), nil, hooks{
		before: func(_, newpath string) error {
			if filepath.Base(newpath) == fileImage && filepath.Dir(newpath) == repo.Dir {
				if !syncedBefore {
					t.Fatal("directory was not synced before rename")
				}
				renamed = true
			}
			return nil
		},
		sync: func(dir string) error {
			if dir != repo.Dir {
				return nil
			}
			if renamed {
				syncedAfter = true
			} else {
				syncedBefore = true
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !renamed || !syncedAfter {
		t.Fatal("directory was not synced after rename")
	}
	assertBytes(t, repo.ImagePath(), data)
}

func TestPublishSameBytesWithChainStillArchives(t *testing.T) {
	repo := testRepo(t)
	data := []byte("same-bytes")
	old := seedActive(t, repo, data, true)
	if err := repo.Publish(data, metaFor(data), nil); err != nil {
		t.Fatal(err)
	}
	arch, err := repo.ArchivePath(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, filepath.Join(arch, fileImage), data)
	loaded, err := Load(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Chain) != 0 || loaded.ID != old.ID {
		t.Fatalf("active meta = %+v", loaded)
	}
}

func TestPublishRestoresMetaLeftInHold(t *testing.T) {
	repo := testRepo(t)
	oldData := []byte("meta-left-in-hold")
	old := seedActive(t, repo, oldData, true)
	hold, err := repo.HoldPath(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(hold, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(repo.MetaPath(), filepath.Join(hold, fileMeta)); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(repo.Dir, dirIncr), filepath.Join(hold, dirIncr)); err != nil {
		t.Fatal(err)
	}
	next := []byte("after-meta-restore")
	if err := repo.Publish(next, metaFor(next), nil); err != nil {
		t.Fatal(err)
	}
	assertBytes(t, repo.ImagePath(), next)
	arch, err := repo.ArchivePath(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, filepath.Join(arch, fileImage), oldData)
	loaded, err := Load(filepath.Join(arch, fileMeta))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != old.ID {
		t.Fatalf("archived meta id = %s", loaded.ID)
	}
	archivedIncr, err := chainIncr(arch, old.Chain[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, archivedIncr, []byte("layer"))
}

func TestPublishReplacesMetaThatDoesNotMatchImage(t *testing.T) {
	repo := testRepo(t)
	oldData := []byte("image-restored-first")
	old := seedActive(t, repo, oldData, true)
	hold, err := repo.HoldPath(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(hold, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(repo.MetaPath(), filepath.Join(hold, fileMeta)); err != nil {
		t.Fatal(err)
	}
	if err := Store(repo.MetaPath(), metaFor([]byte("new-meta-still-active"))); err != nil {
		t.Fatal(err)
	}
	next := []byte("after-meta-mismatch")
	if err := repo.Publish(next, metaFor(next), nil); err != nil {
		t.Fatal(err)
	}
	assertBytes(t, repo.ImagePath(), next)
	arch, err := repo.ArchivePath(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, filepath.Join(arch, fileImage), oldData)
	loaded, err := Load(filepath.Join(arch, fileMeta))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != old.ID || len(loaded.Chain) != 1 {
		t.Fatalf("archived meta = %+v", loaded)
	}
}

func TestPublishArchiveRenameKeepsPreviousAndRetries(t *testing.T) {
	repo := testRepo(t)
	oldData := []byte("chain-waiting-in-hold")
	old := seedActive(t, repo, oldData, true)
	arch, err := repo.ArchivePath(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(arch, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(arch, fileImage), []byte("older-archive"), 0o644); err != nil {
		t.Fatal(err)
	}
	next := []byte("active-while-archive-rename-fails")
	err = publish(repo, next, metaFor(next), nil, hooks{
		rename: func(oldpath, newpath string) error {
			if newpath == arch {
				return errors.New("archive rename failed")
			}
			return os.Rename(oldpath, newpath)
		},
	})
	if err == nil {
		t.Fatal("expected archive rename failure")
	}
	assertBytes(t, repo.ImagePath(), next)
	assertBytes(t, filepath.Join(arch, fileImage), []byte("older-archive"))
	hold, err := repo.HoldPath(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, filepath.Join(hold, fileImage), oldData)
	if _, statErr := os.Stat(arch + archiveParkSuffix); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("parked archive err = %v", statErr)
	}
	third := []byte("publishes-the-stranded-hold")
	if err := repo.Publish(third, metaFor(third), nil); err != nil {
		t.Fatal(err)
	}
	assertBytes(t, repo.ImagePath(), third)
	assertBytes(t, filepath.Join(arch, fileImage), oldData)
	archivedIncr, err := chainIncr(arch, old.Chain[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, archivedIncr, []byte("layer"))
	if _, statErr := os.Stat(hold); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("hold still present: %v", statErr)
	}
}

func TestPublishRestoresParkedArchive(t *testing.T) {
	repo := testRepo(t)
	active := []byte("active-beside-parked-archive")
	if err := repo.Publish(active, metaFor(active), nil); err != nil {
		t.Fatal(err)
	}
	retired := metaFor([]byte("retired-chain"))
	arch, err := repo.ArchivePath(retired.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(arch, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(arch, fileImage), []byte("park-me"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(arch, arch+archiveParkSuffix); err != nil {
		t.Fatal(err)
	}
	next := []byte("after-parked-archive")
	if err := repo.Publish(next, metaFor(next), nil); err != nil {
		t.Fatal(err)
	}
	assertBytes(t, filepath.Join(arch, fileImage), []byte("park-me"))
	if _, statErr := os.Stat(arch + archiveParkSuffix); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("parked archive err = %v", statErr)
	}
	assertBytes(t, repo.ImagePath(), next)
}

func TestPublishSyncAfterImageRenameClearsFreshSlot(t *testing.T) {
	repo := testRepo(t)
	data := []byte("sync-fails-after-image")
	err := publish(repo, data, metaFor(data), nil, failSyncAfter(repo.Dir, fileImage))
	if err == nil {
		t.Fatal("expected directory sync failure")
	}
	if _, statErr := os.Stat(repo.ImagePath()); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("active image err = %v", statErr)
	}
	if _, statErr := os.Stat(repo.MetaPath()); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("active meta err = %v", statErr)
	}
	assertNoTemps(t, repo.Dir)
	if err := repo.Publish(data, metaFor(data), nil); err != nil {
		t.Fatal(err)
	}
	assertBytes(t, repo.ImagePath(), data)
}

func TestPublishSyncAfterMetaRenameClearsFreshSlot(t *testing.T) {
	repo := testRepo(t)
	data := []byte("sync-fails-after-meta")
	err := publish(repo, data, metaFor(data), nil, failSyncAfter(repo.Dir, fileMeta))
	if err == nil {
		t.Fatal("expected directory sync failure")
	}
	if _, statErr := os.Stat(repo.ImagePath()); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("active image err = %v", statErr)
	}
	if _, statErr := os.Stat(repo.MetaPath()); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("active meta err = %v", statErr)
	}
	assertNoTemps(t, repo.Dir)
	if err := repo.Publish(data, metaFor(data), nil); err != nil {
		t.Fatal(err)
	}
	assertBytes(t, repo.ImagePath(), data)
}

func TestPublishSyncAfterMetaRenameRestoresPrevious(t *testing.T) {
	repo := testRepo(t)
	oldData := []byte("kept-across-meta-sync")
	old := seedActive(t, repo, oldData, true)
	err := publish(repo, []byte("incoming"), metaFor([]byte("incoming")), nil, failSyncAfter(repo.Dir, fileMeta))
	if err == nil {
		t.Fatal("expected directory sync failure")
	}
	assertBytes(t, repo.ImagePath(), oldData)
	loaded, err := Load(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != old.ID || len(loaded.Chain) != 1 {
		t.Fatalf("active meta = %+v", loaded)
	}
	incr, err := repo.IncrPath(old.Chain[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, incr, []byte("layer"))
	if _, statErr := os.Stat(filepath.Join(repo.Dir, dirHold, old.ID)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("hold still present: %v", statErr)
	}
}

func TestPublishRepairsOrphanMetaWithPreviousHold(t *testing.T) {
	repo := testRepo(t)
	oldData := []byte("previous-chain-in-hold")
	old := seedActive(t, repo, oldData, true)
	hold, err := repo.HoldPath(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(hold, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{fileImage, fileMeta, dirIncr} {
		if err := os.Rename(filepath.Join(repo.Dir, name), filepath.Join(hold, name)); err != nil {
			t.Fatal(err)
		}
	}
	orphan := metaFor([]byte("meta-without-image"))
	if err := Store(repo.MetaPath(), orphan); err != nil {
		t.Fatal(err)
	}
	next := []byte("retry-after-orphan-meta")
	if err := repo.Publish(next, metaFor(next), nil); err != nil {
		t.Fatal(err)
	}
	assertBytes(t, repo.ImagePath(), next)
	loaded, err := Load(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID == orphan.ID {
		t.Fatal("orphan meta stayed active")
	}
	arch, err := repo.ArchivePath(old.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, filepath.Join(arch, fileImage), oldData)
	archivedIncr, err := chainIncr(arch, old.Chain[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, archivedIncr, []byte("layer"))
}

func TestPublishArchivesTornIncrBesideAnotherHold(t *testing.T) {
	repo := testRepo(t)
	layerA := "aaaaaaaaaaaaaaaa"
	layerB := "bbbbbbbbbbbbbbbb"
	dataA := []byte("image-a")
	metaA := metaFor(dataA)
	metaA.Chain = []ChainEntry{validEntry(metaA.ID, layerA)}
	if err := repo.Publish(dataA, metaA, nil); err != nil {
		t.Fatal(err)
	}
	incrA, err := repo.IncrPath(layerA)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(incrA), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(incrA, []byte("layer-a"), 0o644); err != nil {
		t.Fatal(err)
	}
	dataB := []byte("image-b")
	metaB := metaFor(dataB)
	metaB.Chain = []ChainEntry{validEntry(metaB.ID, layerB)}
	refs := holdSet{ids: map[string]bool{metaA.ID: true}}
	if err := repo.Publish(dataB, metaB, refs); err != nil {
		t.Fatal(err)
	}
	incrB, err := repo.IncrPath(layerB)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(incrB), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(incrB, []byte("layer-b"), 0o644); err != nil {
		t.Fatal(err)
	}
	holdB, err := repo.HoldPath(metaB.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(holdB, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(repo.ImagePath(), filepath.Join(holdB, fileImage)); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(repo.MetaPath(), filepath.Join(holdB, fileMeta)); err != nil {
		t.Fatal(err)
	}
	dataC := []byte("image-c")
	metaC := metaFor(dataC)
	if err := repo.Publish(dataC, metaC, nil); err != nil {
		t.Fatal(err)
	}
	archB, err := repo.ArchivePath(metaB.ID)
	if err != nil {
		t.Fatal(err)
	}
	archivedB, err := chainIncr(archB, layerB)
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, archivedB, []byte("layer-b"))
	if _, statErr := os.Stat(incrB); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("active incr err = %v", statErr)
	}
	archA, err := repo.ArchivePath(metaA.ID)
	if err != nil {
		t.Fatal(err)
	}
	archivedA, err := chainIncr(archA, layerA)
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, archivedA, []byte("layer-a"))
	dataD := []byte("image-d")
	if err := repo.Publish(dataD, metaFor(dataD), nil); err != nil {
		t.Fatal(err)
	}
	archC, err := repo.ArchivePath(metaC.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(filepath.Join(archC, dirIncr, layerB+squashExt)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("archived C incr err = %v", statErr)
	}
}

func TestPublishSweepKeepsForeignLayersOutOfArchives(t *testing.T) {
	repo := testRepo(t)
	layerB := "bbbbbbbbbbbbbbbb"
	layerC := "cccccccccccccccc"
	dataA := []byte("image-a")
	metaA := metaFor(dataA)
	if err := repo.Publish(dataA, metaA, nil); err != nil {
		t.Fatal(err)
	}
	dataB := []byte("image-b")
	metaB := metaFor(dataB)
	metaB.Chain = []ChainEntry{validEntry(metaB.ID, layerB)}
	refsA := holdSet{ids: map[string]bool{metaA.ID: true}}
	if err := repo.Publish(dataB, metaB, refsA); err != nil {
		t.Fatal(err)
	}
	writeLayer(t, repo, layerB, []byte("layer-b"))
	dataC := []byte("image-c")
	metaC := metaFor(dataC)
	metaC.Chain = []ChainEntry{validEntry(metaC.ID, layerC)}
	refsAB := holdSet{ids: map[string]bool{metaA.ID: true, metaB.ID: true}}
	if err := repo.Publish(dataC, metaC, refsAB); err != nil {
		t.Fatal(err)
	}
	writeLayer(t, repo, layerC, []byte("layer-c"))
	holdC := tearActive(t, repo, metaC.ID)
	refsC := holdSet{ids: map[string]bool{metaC.ID: true}}
	dataD := []byte("image-d")
	if err := repo.Publish(dataD, metaFor(dataD), refsC); err != nil {
		t.Fatal(err)
	}
	assertBytes(t, repo.ImagePath(), dataD)
	archA, err := repo.ArchivePath(metaA.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(filepath.Join(archA, dirIncr)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("plain archive A adopted foreign layers: %v", statErr)
	}
	archB, err := repo.ArchivePath(metaB.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, filepath.Join(archB, dirIncr, layerB+squashExt), []byte("layer-b"))
	if _, statErr := os.Stat(filepath.Join(archB, dirIncr, layerC+squashExt)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("archive B adopted foreign layers: %v", statErr)
	}
	assertBytes(t, filepath.Join(holdC, dirIncr, layerC+squashExt), []byte("layer-c"))
	if _, statErr := os.Stat(filepath.Join(repo.Dir, dirIncr)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("active incr err = %v", statErr)
	}
}

func TestPublishSweepsTornIncrIntoReferencedHold(t *testing.T) {
	repo := testRepo(t)
	layerC := "cccccccccccccccc"
	dataC := []byte("image-c")
	metaC := metaFor(dataC)
	metaC.Chain = []ChainEntry{validEntry(metaC.ID, layerC)}
	if err := repo.Publish(dataC, metaC, nil); err != nil {
		t.Fatal(err)
	}
	writeLayer(t, repo, layerC, []byte("layer-c"))
	holdC := tearActive(t, repo, metaC.ID)
	refs := holdSet{ids: map[string]bool{metaC.ID: true}}
	dataD := []byte("image-d")
	metaD := metaFor(dataD)
	if err := repo.Publish(dataD, metaD, refs); err != nil {
		t.Fatal(err)
	}
	assertBytes(t, repo.ImagePath(), dataD)
	assertBytes(t, filepath.Join(holdC, dirIncr, layerC+squashExt), []byte("layer-c"))
	if _, statErr := os.Stat(filepath.Join(repo.Dir, dirIncr)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("active incr err = %v", statErr)
	}
	dataE := []byte("image-e")
	if err := repo.Publish(dataE, metaFor(dataE), refs); err != nil {
		t.Fatal(err)
	}
	archD, err := repo.ArchivePath(metaD.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(filepath.Join(archD, dirIncr)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("archive D captured foreign layers: %v", statErr)
	}
	assertBytes(t, filepath.Join(holdC, dirIncr, layerC+squashExt), []byte("layer-c"))
}

func TestPublishSweepResolvesAmbiguousHoldsByChain(t *testing.T) {
	repo := testRepo(t)
	layerC := "cccccccccccccccc"
	dataA := []byte("image-a")
	metaA := metaFor(dataA)
	if err := repo.Publish(dataA, metaA, nil); err != nil {
		t.Fatal(err)
	}
	dataB := []byte("image-b")
	metaB := metaFor(dataB)
	refsA := holdSet{ids: map[string]bool{metaA.ID: true}}
	if err := repo.Publish(dataB, metaB, refsA); err != nil {
		t.Fatal(err)
	}
	dataC := []byte("image-c")
	metaC := metaFor(dataC)
	metaC.Chain = []ChainEntry{validEntry(metaC.ID, layerC)}
	refsAB := holdSet{ids: map[string]bool{metaA.ID: true, metaB.ID: true}}
	if err := repo.Publish(dataC, metaC, refsAB); err != nil {
		t.Fatal(err)
	}
	writeLayer(t, repo, layerC, []byte("layer-c"))
	tearActive(t, repo, metaC.ID)
	dataD := []byte("image-d")
	if err := repo.Publish(dataD, metaFor(dataD), nil); err != nil {
		t.Fatal(err)
	}
	archC, err := repo.ArchivePath(metaC.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, filepath.Join(archC, dirIncr, layerC+squashExt), []byte("layer-c"))
	for _, id := range []string{metaA.ID, metaB.ID} {
		arch, err := repo.ArchivePath(id)
		if err != nil {
			t.Fatal(err)
		}
		if _, statErr := os.Stat(filepath.Join(arch, dirIncr)); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("plain archive %s adopted foreign layers: %v", id, statErr)
		}
	}
	dataE := []byte("image-e")
	if err := repo.Publish(dataE, metaFor(dataE), nil); err != nil {
		t.Fatal(err)
	}
	archD, err := repo.ArchivePath(metaFor(dataD).ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(filepath.Join(archD, dirIncr)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("archive D captured foreign layers: %v", statErr)
	}
}

func TestPublishRepublishReturnsLayersFromReferencedHold(t *testing.T) {
	repo := testRepo(t)
	layer := "aaaaaaaaaaaaaaaa"
	dataA := []byte("image-a")
	metaA := metaFor(dataA)
	metaA.Chain = []ChainEntry{validEntry(metaA.ID, layer)}
	if err := repo.Publish(dataA, metaA, nil); err != nil {
		t.Fatal(err)
	}
	writeLayer(t, repo, layer, []byte("layer-a"))
	dataB := []byte("image-b")
	refs := holdSet{ids: map[string]bool{metaA.ID: true}}
	if err := repo.Publish(dataB, metaFor(dataB), refs); err != nil {
		t.Fatal(err)
	}
	holdA, err := repo.HoldPath(metaA.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, filepath.Join(holdA, dirIncr, layer+squashExt), []byte("layer-a"))
	if err := repo.Publish(dataA, metaA, refs); err != nil {
		t.Fatal(err)
	}
	assertBytes(t, repo.ImagePath(), dataA)
	loaded, err := Load(repo.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Chain) != 1 || loaded.Chain[0].ID != layer {
		t.Fatalf("active meta = %+v", loaded)
	}
	incr, err := repo.IncrPath(layer)
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, incr, []byte("layer-a"))
	if _, statErr := os.Stat(holdA); statErr != nil {
		t.Fatalf("referenced hold deleted: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(holdA, dirIncr, layer+squashExt)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("hold kept a duplicate layer: %v", statErr)
	}
	dataC := []byte("image-c")
	if err := repo.Publish(dataC, metaFor(dataC), nil); err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(holdA); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("hold survived the dropped reference: %v", statErr)
	}
	archA, err := repo.ArchivePath(metaA.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, filepath.Join(archA, fileImage), dataA)
	assertBytes(t, filepath.Join(archA, dirIncr, layer+squashExt), []byte("layer-a"))
}

func TestPublishReplaceKeepsReferencedHoldLayers(t *testing.T) {
	repo := testRepo(t)
	layer := "aaaaaaaaaaaaaaaa"
	dataA := []byte("image-a")
	metaA := metaFor(dataA)
	metaA.Chain = []ChainEntry{validEntry(metaA.ID, layer)}
	if err := repo.Publish(dataA, metaA, nil); err != nil {
		t.Fatal(err)
	}
	writeLayer(t, repo, layer, []byte("layer-a"))
	dataB := []byte("image-b")
	refs := holdSet{ids: map[string]bool{metaA.ID: true}}
	if err := repo.Publish(dataB, metaFor(dataB), refs); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after A became active again but before the
	// stale hold was processed: the active slot shows A while
	// _hold/<A> still owns the image, the meta, and the only copy of
	// the layer.
	if err := os.WriteFile(repo.ImagePath(), dataA, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Store(repo.MetaPath(), metaA); err != nil {
		t.Fatal(err)
	}
	dataC := []byte("image-c")
	if err := repo.Publish(dataC, metaFor(dataC), refs); err != nil {
		t.Fatal(err)
	}
	assertBytes(t, repo.ImagePath(), dataC)
	holdA, err := repo.HoldPath(metaA.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, filepath.Join(holdA, fileImage), dataA)
	assertBytes(t, filepath.Join(holdA, dirIncr, layer+squashExt), []byte("layer-a"))
	if _, statErr := os.Stat(filepath.Join(repo.Dir, dirIncr)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("active incr err = %v", statErr)
	}
}

func failSyncAfter(dir, base string) hooks {
	var armed bool
	return hooks{
		before: func(_, newpath string) error {
			if filepath.Base(newpath) == base && filepath.Dir(newpath) == dir {
				armed = true
			}
			return nil
		},
		sync: func(syncDir string) error {
			if armed && syncDir == dir {
				armed = false
				return errors.New("directory sync failed")
			}
			return nil
		},
	}
}

type holdSet struct {
	ids map[string]bool
}

func (h holdSet) Holds(ref Ref) (bool, error) {
	return h.ids[ref.FullID], nil
}

func (h holdSet) Holder(ref Ref) (string, bool, error) {
	if h.ids[ref.FullID] {
		return "pinning-session", true, nil
	}
	return "", false, nil
}

func testRepo(t *testing.T) Repo {
	t.Helper()
	repo, err := OpenRepo(t.TempDir(), "alice/app")
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

func metaFor(data []byte) Meta {
	sum := sha256.Sum256(data)
	meta := validMeta()
	meta.ID = ID(sum)
	meta.SHA256 = hex.EncodeToString(sum[:])
	meta.Size = int64(len(data))
	meta.Chain = nil
	return meta
}

func seedActive(t *testing.T, repo Repo, data []byte, withChain bool) Meta {
	t.Helper()
	meta := metaFor(data)
	meta.Name = repo.Name()
	if withChain {
		const layer = "fedcba9876543210"
		meta.Chain = []ChainEntry{validEntry(meta.ID, layer)}
	}
	if err := os.MkdirAll(repo.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(repo.ImagePath(), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Store(repo.MetaPath(), meta); err != nil {
		t.Fatal(err)
	}
	if withChain {
		incr, err := repo.IncrPath(meta.Chain[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(incr), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(incr, []byte("layer"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return meta
}

func chainIncr(chainDir, id string) (string, error) {
	if err := checkID(id); err != nil {
		return "", err
	}
	return filepath.Join(chainDir, dirIncr, id+squashExt), nil
}

func writeLayer(t *testing.T, repo Repo, id string, data []byte) {
	t.Helper()
	incr, err := repo.IncrPath(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(incr), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(incr, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// tearActive simulates a replace that crashed after image and meta
// reached _hold/<id> but before _incr/ did. It returns the hold path.
func tearActive(t *testing.T, repo Repo, id string) string {
	t.Helper()
	hold, err := repo.HoldPath(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(hold, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(repo.ImagePath(), filepath.Join(hold, fileImage)); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(repo.MetaPath(), filepath.Join(hold, fileMeta)); err != nil {
		t.Fatal(err)
	}
	return hold
}

func assertBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}

func assertNoTemps(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if len(entry.Name()) > 0 && entry.Name()[0] == '.' {
			t.Errorf("temp file left behind: %s", entry.Name())
		}
	}
}
