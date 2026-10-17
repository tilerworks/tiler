package image

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// buildMembers is the multi-member full build pipeline: partition
// the source by top-level directory, pack each partition into its
// own complete squashfs inside a scratch payload directory, and
// publish the directory payload atomically. The scratch directory
// never survives the call.
func buildMembers(req FullRequest) (FullResult, error) {
	repo, err := validateFull(req)
	if err != nil {
		return FullResult{}, err
	}
	if err := prepareScene(req); err != nil {
		return FullResult{}, err
	}
	names, err := memberPartitions(req.SrcDir)
	if err != nil {
		return FullResult{}, err
	}
	payloadTmp, err := makePayloadTmpDir(repo, tempMembersDirName)
	if err != nil {
		return FullResult{}, err
	}
	defer func() { _ = os.RemoveAll(payloadTmp) }()
	meta, err := packMembers(repo, req, payloadTmp, names)
	if err != nil {
		return FullResult{}, err
	}
	return publishMembers(repo, req, payloadTmp, meta)
}

// memberPartitions enumerates the top-level directories of src in
// sorted order. Every entry must be a directory with a legal member
// name; anything else is refused with guidance to the blocks form,
// and an empty partition set is refused.
func memberPartitions(src string) ([]string, error) {
	entries, err := os.ReadDir(src)
	if err != nil {
		return nil, fmt.Errorf("build: source: %w", err)
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() {
			return nil, fmt.Errorf("build: source entry %s is not a directory; "+
				"the members form partitions top-level directories only; "+
				"use split_mode blocks to store a mixed source", entry.Name())
		}
		if err := checkSegment(entry.Name()); err != nil {
			return nil, fmt.Errorf(
				"build: source entry %s is not a legal member name", entry.Name())
		}
		names = append(names, entry.Name())
	}
	if len(names) == 0 {
		return nil, errors.New("build: source has no top-level directories to partition")
	}
	return names, nil
}

// packMembers packs every partition into its own squashfs inside the
// scratch payload directory and assembles the members metadata from
// the bytes that landed on disk. The whole-image identity is the
// derived manifest digest, so the same partition set always derives
// the same id.
func packMembers(repo Repo, req FullRequest, payloadTmp string, names []string) (Meta, error) {
	run := toolRunner(req)
	meta := newFullMeta(repo, req, buildNow(req))
	for _, name := range names {
		entry, err := packMember(run, req.SrcDir, payloadTmp, name)
		if err != nil {
			return Meta{}, err
		}
		if entry.MtimeUnix > meta.MtimeUnix {
			meta.MtimeUnix = entry.MtimeUnix
		}
		meta.Members = append(meta.Members, entry)
	}
	meta.SHA256 = membersDigest(meta.Members)
	meta.ID = meta.SHA256[:idHexLen]
	var total int64
	for _, entry := range meta.Members {
		total += entry.Size
	}
	meta.Size = total
	if err := meta.Validate(); err != nil {
		return Meta{}, fmt.Errorf("build: %w", err)
	}
	return meta, nil
}

// packMember packs one top-level directory with the same packer
// arguments as the single-file build and records the manifest facts
// from the file that landed on disk. The failures route through the
// single-file limit classifier.
func packMember(run ToolRunner, src, payloadTmp, name string) (MemberEntry, error) {
	dst := filepath.Join(payloadTmp, name+squashExt)
	if err := packFullImage(run, filepath.Join(src, name), dst); err != nil {
		return MemberEntry{}, failTool(dst, err)
	}
	if err := syncFile(dst); err != nil {
		return MemberEntry{}, failWrite(dst, err)
	}
	sum, err := fileSHA256(dst)
	if err != nil {
		return MemberEntry{}, fmt.Errorf("build: %w", err)
	}
	info, err := os.Stat(dst)
	if err != nil {
		return MemberEntry{}, fmt.Errorf("build: %w", err)
	}
	return MemberEntry{
		Name:      name,
		Image:     memberImageName(name),
		Size:      info.Size(),
		SHA256:    hexDigest(sum),
		MtimeUnix: info.ModTime().Unix(),
	}, nil
}

// publishMembers publishes the scratch members directory with one
// rename and re-hashes every published member file afterwards.
func publishMembers(
	repo Repo,
	req FullRequest,
	payloadTmp string,
	meta Meta,
) (FullResult, error) {
	body, err := Encode(meta)
	if err != nil {
		return FullResult{}, fmt.Errorf("build: %w", err)
	}
	// The final place cannot fail here, but resolving it before the
	// meta temp exists keeps staged temps from leaking on any error.
	finalDir := repo.MembersDirPath()
	metaTmp, err := writeTemp(repo.Dir, fileMeta, body, hooks{})
	if err != nil {
		return FullResult{}, fmt.Errorf("build: %w", err)
	}
	if err := publishStagedPayload(repo, meta, req.Refs,
		payloadTmp, finalDir, metaTmp, hooks{}); err != nil {
		return FullResult{}, err
	}
	if err := verifyPublishedMembers(repo, meta); err != nil {
		return FullResult{}, err
	}
	return FullResult{ID: meta.ID, Path: finalDir, SHA256: meta.SHA256}, nil
}

// verifyPublishedMembers re-hashes every published member against
// the manifest and recomputes the derived whole digest, extending
// the discipline of hashing the image again after the rename.
func verifyPublishedMembers(repo Repo, meta Meta) error {
	ok, err := dirMatchesMembers(repo.MembersDirPath(), meta.Members)
	if err != nil {
		return fmt.Errorf("build: %w", err)
	}
	if !ok || membersDigest(meta.Members) != meta.SHA256 {
		return fmt.Errorf("build: %s does not match the members manifest",
			repo.MembersDirPath())
	}
	return nil
}
