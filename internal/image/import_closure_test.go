package image

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestPlanImportClosureSelectsAncestorStacks(t *testing.T) {
	graph := closureGraph()
	dir := writeClosureDir(t, graph.nodes)
	// Latest keeps root, mid, and tip. The other branches have no
	// payload objects; a dangling entry fails the test if planning
	// stats them.
	danglePayloads(t, dir, []string{graph.zero, graph.branch, graph.other, graph.child})
	sealDir(t, dir)
	before := importStamp(t, dir)
	images := t.TempDir()
	var latest ClosurePlan
	for _, mode := range []ImportMode{ImportLink, ImportCopy} {
		req := func(hash string) ImportRequest {
			return closureRequest(images, dir, hash, mode)
		}
		latest = mustClosure(t, req(""))
		requireClosureIDs(t, latest, graph.root, graph.mid, graph.tip)
		requireClosureIDs(t, mustClosure(t, req(graph.root[:4])), graph.root)
		requireClosureIDs(t, mustClosure(t, req(graph.mid[:8])), graph.root, graph.mid)
		requireClosureIDs(t, mustClosure(t, req(strings.ToUpper(graph.tip))), graph.root, graph.mid, graph.tip)
		requireClosureIDs(t, mustClosure(t, req(graph.other)), graph.other)
		requireClosureIDs(t, mustClosure(t, req(graph.child[:4])), graph.other, graph.child)
		requireClosureIDs(t, mustClosure(t, req(graph.zero[:4])), graph.zero)
		requireClosureIDs(t, mustClosure(t, req(importHashAll)),
			graph.zero, graph.root, graph.branch, graph.mid, graph.tip, graph.other, graph.child)
		if latest.Snapshot.Plan.Mode != mode || latest.Snapshot.Plan.Kind != SourceDir {
			t.Fatalf("snapshot = %+v", latest.Snapshot.Plan)
		}
	}
	tip := closureNodeByID(latest.Nodes, graph.tip)
	root := closureNodeByID(latest.Nodes, graph.root)
	if tip.Parent != graph.mid || tip.Kind != KindIncremental || tip.Format.Type != FormatErofs ||
		tip.Description != "split tip" || len(tip.Blocks) != 2 ||
		tip.Blocks[0].Name != "blk.000" || tip.Blocks[1].Name != "blk.001" ||
		tip.Blocks[0].SHA256 != strings.Repeat("1", 64) {
		t.Fatalf("tip facts = %+v", tip)
	}
	if root.Kind != KindFull || root.Description != "Vendor base" || root.Builder != "vendor" ||
		root.ContentSHA256 != strings.Repeat("3", 64) || len(root.Blocks) != 0 ||
		!root.CreatedAt.Equal(time.Unix(10, 0).UTC()) {
		t.Fatalf("root facts = %+v", root)
	}
	zero := closureNodeByID(latest.Snapshot.Manifest.Images, graph.zero)
	if !IsUnknownSHA256(zero.ID) || zero.Description != "unknown link" || len(zero.Blocks) != 0 {
		t.Fatalf("unknown root = %+v", zero)
	}
	if latest.Snapshot.Manifest.Name != "other/lib" || latest.Snapshot.Plan.Name != "vendor/app" ||
		latest.Snapshot.Plan.Location != dir || !bytes.Equal(latest.Snapshot.Body, mustRead(t, filepath.Join(dir, fileMeta))) {
		t.Fatalf("snapshot identity = %+v", latest.Snapshot.Plan)
	}
	latest.Nodes[2].Blocks[0].Name = "mutated"
	if closureNodeByID(latest.Snapshot.Manifest.Images, graph.tip).Blocks[0].Name != "blk.000" {
		t.Fatal("closure aliases the snapshot")
	}
	if _, err := os.Stat(latest.Snapshot.Plan.Repo.Dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("planning created target: %v", err)
	}
	if after := importStamp(t, dir); after != before {
		t.Fatalf("source tree changed\n%s\n%s", before, after)
	}
}

func TestPlanImportClosureRejectsSelectorsAndMetadata(t *testing.T) {
	withLinuxLinkPolicy(t)
	images := t.TempDir()
	graph := closureGraph()
	dir := writeClosureDir(t, graph.nodes)
	base := mustRead(t, filepath.Join(dir, fileMeta))
	req := func(source, hash string) ImportRequest {
		return closureRequest(images, source, hash, ImportLink)
	}
	file := writeFakeSquashfs(t, t.TempDir(), []byte("payload"))
	if _, err := PlanImportClosure(req(file, "")); err == nil || !strings.Contains(err.Error(), "image directory") {
		t.Fatalf("file closure: %v", err)
	}
	empty := writeClosureDir(t, nil)
	for _, hash := range []string{"", importHashAll} {
		if _, err := PlanImportClosure(req(empty, hash)); err == nil || !strings.Contains(err.Error(), "no image versions") {
			t.Fatalf("empty %q: %v", hash, err)
		}
	}
	for _, hash := range []string{"abc", "ALL", "all ", "zzzz"} {
		_, err := PlanImportClosure(req(dir, hash))
		if !errors.Is(err, ErrInvalidHashPrefix) {
			t.Fatalf("hash %q: %v", hash, err)
		}
	}
	if _, err := PlanImportClosure(req(dir, strings.Repeat("9", 8))); err == nil || !strings.Contains(err.Error(), "no matching") {
		t.Fatalf("missing prefix: %v", err)
	}
	left, right := "abcd"+strings.Repeat("1", 60), "abcd"+strings.Repeat("2", 60)
	ambDir := writeClosureDir(t, []ImageMeta{
		closureNode(left, KindFull, "", 1, closureSquash(), nil),
		closureNode(right, KindFull, "", 2, closureSquash(), nil),
	})
	_, err := PlanImportClosure(req(ambDir, "ABCD"))
	var amb *AmbiguousVersionError
	if !errors.As(err, &amb) || !slices.Equal(amb.Candidates, []string{left, right}) {
		t.Fatalf("ambiguous: %v", err)
	}
	edits := []struct {
		name string
		body []byte
		want string
	}{
		{name: "kind", body: bytes.Replace(base, []byte("kind: incremental"), []byte("kind: layer"), 1), want: "kind"},
		{name: "cycle", body: bytes.Replace(base, []byte("parent: "+graph.mid), []byte("parent: "+graph.tip), 1), want: "acyclic"},
		{name: "parent", body: bytes.Replace(base, []byte("parent: "+graph.root), []byte("parent: "+strings.Repeat("9", 64)), 1), want: "parent"},
		{name: "duplicate", body: bytes.ReplaceAll(base, []byte(graph.branch), []byte(graph.root)), want: "duplicate"},
		{name: "block", body: bytes.Replace(base, []byte("blk.000"), []byte("blk.002"), 1), want: "blk.000"},
		{name: "version", body: bytes.Replace(base, []byte("version: 1\n"), []byte("version: 2\n"), 1), want: "unsupported"},
		{name: "unselected id", body: bytes.ReplaceAll(base, []byte(graph.child), []byte(strings.Repeat("F", 64))), want: "lowercase"},
		{name: "block digest", body: bytes.Replace(base, []byte(strings.Repeat("1", 64)), []byte(UnknownSHA256), 1), want: "unknown digest"},
	}
	for _, edit := range edits {
		bad := writeRawClosure(t, edit.body)
		_, err := PlanImportClosure(req(bad, ""))
		if err == nil || !strings.Contains(err.Error(), edit.want) {
			t.Fatalf("%s: %v", edit.name, err)
		}
	}
	incr := closureNode(graph.branch, KindIncremental, graph.root, 2, closureSquash(), nil)
	incr.ID, incr.SHA256 = UnknownSHA256, UnknownSHA256
	unknownDir := writeClosureDir(t, []ImageMeta{
		closureNode(graph.root, KindFull, "", 1, closureSquash(), nil), incr,
	})
	if _, err := PlanImportClosure(req(unknownDir, graph.root[:4])); err == nil || !strings.Contains(err.Error(), "unknown digest") {
		t.Fatalf("unselected unknown incremental: %v", err)
	}
	split := closureNode(UnknownSHA256, KindFull, "", 1, closureSquash(), closureBlocks())
	if _, err := PlanImportClosure(req(writeClosureDir(t, []ImageMeta{split}), "")); err == nil || !strings.Contains(err.Error(), "unknown digest") {
		t.Fatalf("unknown split: %v", err)
	}
}

type closureIDs struct {
	zero, root, branch, mid, tip, other, child string
	nodes                                      []ImageMeta
}

func closureGraph() closureIDs {
	g := closureIDs{
		zero: UnknownSHA256, root: strings.Repeat("a", 64), branch: strings.Repeat("b", 64),
		mid: strings.Repeat("c", 64), tip: strings.Repeat("d", 64),
		other: strings.Repeat("e", 64), child: strings.Repeat("f", 64),
	}
	sq := closureSquash()
	g.nodes = []ImageMeta{
		closureNode(g.zero, KindFull, "", 1, sq, nil),
		closureNode(g.root, KindFull, "", 10, sq, nil),
		closureNode(g.branch, KindIncremental, g.root, 20, sq, nil),
		closureNode(g.mid, KindIncremental, g.root, 30, sq, nil),
		closureNode(g.tip, KindIncremental, g.mid, 40, Format{Type: FormatErofs, Compression: "lz4"}, closureBlocks()),
		closureNode(g.other, KindFull, "", 40, sq, nil),
		closureNode(g.child, KindIncremental, g.other, 30, sq, nil),
	}
	g.nodes[0].Description = "unknown link"
	g.nodes[1].Description = "Vendor base"
	g.nodes[1].ContentSHA256 = strings.Repeat("3", 64)
	g.nodes[4].Description = "split tip"
	return g
}

func closureSquash() Format {
	return Format{Type: FormatSquashfs, Compression: CompressionZstd}
}

func closureNode(id, kind, parent string, created int64, format Format, blocks []BlockEntry) ImageMeta {
	node := ImageMeta{
		ID: id, Kind: kind, Parent: parent, Format: format, Size: 4, SHA256: id,
		MtimeUnix: created, CreatedAt: time.Unix(created, 0).UTC(), Blocks: blocks,
	}
	if kind == KindFull {
		node.Builder = "vendor"
	}
	if len(blocks) > 0 {
		node.Size = 0
		for _, block := range blocks {
			node.Size += block.Size
		}
	}
	return node
}

func closureBlocks() []BlockEntry {
	return []BlockEntry{
		{Name: "blk.000", Size: 2, SHA256: strings.Repeat("1", 64), MtimeUnix: 1},
		{Name: "blk.001", Size: 2, SHA256: strings.Repeat("2", 64), MtimeUnix: 1},
	}
}

func writeClosureDir(t *testing.T, nodes []ImageMeta) string {
	t.Helper()
	body, err := EncodeVersionManifest(VersionManifest{
		Version: VersionManifestVersion, Name: "other/lib", Images: nodes,
	})
	if err != nil {
		t.Fatal(err)
	}
	return writeRawClosure(t, body)
}

func writeRawClosure(t *testing.T, body []byte) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "src")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, fileMeta), body, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func danglePayloads(t *testing.T, dir string, ids []string) {
	t.Helper()
	base := filepath.Join(dir, dirObjects)
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if err := os.Symlink("missing-payload", filepath.Join(base, id)); err != nil {
			t.Fatal(err)
		}
	}
}

func sealDir(t *testing.T, dir string) {
	t.Helper()
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
}

func closureRequest(images, dir, hash string, mode ImportMode) ImportRequest {
	return ImportRequest{Mode: mode, ImagesDir: images, Name: "vendor/app", Source: dir, Hash: hash}
}

func mustClosure(t *testing.T, req ImportRequest) ClosurePlan {
	t.Helper()
	plan, err := PlanImportClosure(req)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func requireClosureIDs(t *testing.T, plan ClosurePlan, ids ...string) {
	t.Helper()
	got := make([]string, len(plan.Nodes))
	for i, node := range plan.Nodes {
		got[i] = node.ID
	}
	if !slices.Equal(got, ids) {
		t.Fatalf("closure = %v, want %v", got, ids)
	}
}

func closureNodeByID(nodes []ImageMeta, id string) ImageMeta {
	for _, node := range nodes {
		if node.ID == id {
			return node
		}
	}
	return ImageMeta{}
}
