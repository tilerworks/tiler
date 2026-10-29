package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/image"
)

func TestMountVerifySkipsUnknownMemberAndChecksKnown(t *testing.T) {
	dir := t.TempDir()
	body := []byte("external-payload")
	payload := filepath.Join(dir, "payload.sqsh")
	if err := os.WriteFile(payload, body, 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(payload)
	if err != nil {
		t.Fatal(err)
	}
	leaf := filepath.Join(dir, "location")
	if err := os.WriteFile(leaf, []byte(payload+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Mode 000 proves the payload was not opened only when Open fails.
	// Root can read that mode, so restore it and do not treat a later
	// success as evidence the hash was skipped.
	if err := os.Chmod(payload, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(payload, 0o644) })
	if opened, openErr := os.Open(payload); openErr == nil {
		_ = opened.Close()
		if err := os.Chmod(payload, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	knownBody := []byte("known-block")
	blockPath := filepath.Join(dir, "blk.000")
	if err := os.WriteFile(blockPath, knownBody, 0o644); err != nil {
		t.Fatal(err)
	}
	binfo, err := os.Stat(blockPath)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(knownBody)
	block := image.BlockFile{
		Path: blockPath, Name: "blk.000", Size: binfo.Size(),
		MtimeUnix: binfo.ModTime().Unix(), SHA256: hex.EncodeToString(sum[:]),
	}
	unknown := image.Member{
		ID: image.UnknownSHA256, Path: leaf, Size: info.Size(),
		MtimeUnix: info.ModTime().Unix(), SHA256: image.UnknownSHA256,
	}
	var notes bytes.Buffer
	prev := mountNotice
	mountNotice = &notes
	t.Cleanup(func() { mountNotice = prev })

	target := image.Target{Library: true, Members: []image.Member{unknown, {Blocks: []image.BlockFile{block}}}}
	if err := verifyTarget(target, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(notes.String(), "digest unknown, skipped sha256 verification") {
		t.Fatalf("notice = %q", notes.String())
	}
	notes.Reset()
	if err := verifyTarget(target, false); err != nil || notes.Len() != 0 {
		t.Fatalf("default probe = %v, notice %q", err, notes.String())
	}
	target.Members[1].Blocks[0].SHA256 = strings.Repeat("ab", 32)
	if err := verifyTarget(target, true); err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("known block = %v", err)
	}
	if err := os.Chmod(payload, 0o644); err != nil {
		t.Fatal(err)
	}
	when := info.ModTime().Add(time.Hour)
	if err := os.Chtimes(payload, when, when); err != nil {
		t.Fatal(err)
	}
	target.Members = []image.Member{unknown}
	if err := verifyTarget(target, true); err == nil {
		t.Fatal("changed source mounted")
	}
	if err := os.Remove(payload); err != nil {
		t.Fatal(err)
	}
	if err := verifyTarget(image.Target{Library: true, Members: []image.Member{unknown}}, true); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing path-ref target = %v", err)
	}
}
