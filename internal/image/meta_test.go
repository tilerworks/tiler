package image

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestIDEqualsPayloadSHA256(t *testing.T) {
	sum := sha256.Sum256([]byte("tiler"))
	got := ID(sum)
	want := hex.EncodeToString(sum[:])
	if got != want {
		t.Fatalf("id = %s, want %s", got, want)
	}
	if len(got) != sumHexLen {
		t.Fatalf("len(id) = %d, want %d", len(got), sumHexLen)
	}
}

func TestIDHexLenFloor(t *testing.T) {
	if idHexLen < 16 {
		t.Fatalf("idHexLen = %d, want >= 16", idHexLen)
	}
}

func testID(id string) string {
	if len(id) == idHexLen && isLowerHex(id, idHexLen) {
		return strings.Repeat(id, sumHexLen/idHexLen)
	}
	return id
}

func validManifest() Manifest {
	const root = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	one := testID("fedcba9876543210")
	two := testID("0011223344556677")
	return Manifest{
		Version: VersionManifestVersion,
		Name:    "alice/app",
		Format:  Format{Type: FormatSquashfs, Compression: CompressionZstd},
		Head:    two,
		Images: []ImageMeta{
			{
				ID:        root,
				Kind:      KindFull,
				Size:      12345,
				SHA256:    root,
				MtimeUnix: 1710000000,
				CreatedAt: time.Date(2026, 10, 16, 8, 0, 0, 0, time.UTC),
				Builder:   "dev",
			},
			{
				ID:        one,
				Kind:      KindIncremental,
				Parent:    root,
				Size:      4096,
				SHA256:    one,
				MtimeUnix: 1710000001,
				CreatedAt: time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC),
			},
			{
				ID:        two,
				Kind:      KindIncremental,
				Parent:    one,
				Size:      4096,
				SHA256:    two,
				MtimeUnix: 1710000002,
				CreatedAt: time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC),
			},
		},
	}
}

// deepManifest is a linear manifest of one full root plus n
// incremental layers, headed by the deepest tip.
func deepManifest(n int) Manifest {
	manifest := validManifest()
	manifest.Images = manifest.Images[:1]
	parent := manifest.Images[0].ID
	for i := range n {
		id := testID(fmt.Sprintf("%016x", i+1))
		manifest.Images = append(manifest.Images, ImageMeta{
			ID:        id,
			Kind:      KindIncremental,
			Parent:    parent,
			Size:      4096,
			SHA256:    id,
			MtimeUnix: int64(1710000001 + i),
			CreatedAt: time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC),
		})
		parent = id
	}
	manifest.Head = parent
	return manifest
}

func TestManifestGraphQueries(t *testing.T) {
	manifest := validManifest()
	root := manifest.Images[0].ID
	one := manifest.Images[1].ID
	two := manifest.Images[2].ID
	unknown := strings.Repeat("e", sumHexLen)

	full, err := manifest.Full()
	if err != nil || full.ID != root {
		t.Fatalf("Full = %+v, %v", full, err)
	}
	if node, ok := manifest.Node(one); !ok || node.Parent != root {
		t.Fatalf("Node(%q) = %+v, %v", one, node, ok)
	}
	if !manifest.Has(two) || manifest.Has(unknown) {
		t.Fatal("Has misreported")
	}
	stack, err := manifest.Stack(one)
	if err != nil {
		t.Fatal(err)
	}
	if len(stack) != 2 || stack[0].ID != root || stack[1].ID != one {
		t.Fatalf("Stack(%q) = %+v, want the root first", one, stack)
	}
	if head, err := manifest.HeadStack(); err != nil || len(head) != 3 {
		t.Fatalf("HeadStack = %+v, %v", head, err)
	}
	if _, err := manifest.Stack(unknown); err == nil ||
		!strings.Contains(err.Error(), "unknown image id") {
		t.Fatalf("unknown tip: %v", err)
	}
	if _, err := manifest.Stack(""); err == nil {
		t.Fatal("empty tip accepted")
	}
	seen := manifest.Reachable(one)
	if !seen[root] || !seen[one] || seen[two] {
		t.Fatalf("Reachable(%q) = %v", one, seen)
	}
	seen = manifest.Reachable(two, unknown)
	if len(seen) != 3 || !seen[root] || !seen[one] || !seen[two] {
		t.Fatalf("Reachable with an unknown tip = %v", seen)
	}
}

func TestManifestStackRejectsBrokenGraphs(t *testing.T) {
	orphan := validManifest()
	orphan.Images = orphan.Images[1:]
	orphan.Images[0].Parent = ""
	if _, err := orphan.Stack(orphan.Images[1].ID); err == nil ||
		!strings.Contains(err.Error(), "does not start at the full root") {
		t.Fatalf("orphan stack: %v", err)
	}
	loop := validManifest()
	loop.Images[1].Parent = loop.Images[2].ID
	if _, err := loop.Stack(loop.Images[1].ID); err == nil ||
		!strings.Contains(err.Error(), "parent cycle") {
		t.Fatalf("cycle: %v", err)
	}
}

func TestManifestFullErrors(t *testing.T) {
	noRoot := validManifest()
	noRoot.Images = noRoot.Images[1:]
	if _, err := noRoot.Full(); err == nil ||
		!strings.Contains(err.Error(), "no full root") {
		t.Fatalf("no root: %v", err)
	}
	twoRoots := validManifest()
	twoRoots.Images[1].Kind = KindFull
	if _, err := twoRoots.Full(); err == nil ||
		!strings.Contains(err.Error(), "more than one full root") {
		t.Fatalf("two roots: %v", err)
	}
}

func TestManifestValidateReportsEveryBranch(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Manifest)
		want   string
	}{
		{
			name:   "duplicate id",
			mutate: func(m *Manifest) { m.Images[2].ID = m.Images[1].ID },
			want:   "images[2].id: duplicate",
		},
		{
			name:   "malformed id",
			mutate: func(m *Manifest) { m.Images[1].ID = strings.Repeat("E", sumHexLen) },
			want:   "images[1].id: must be 64 lowercase hex digits",
		},
		{
			name:   "unknown parent",
			mutate: func(m *Manifest) { m.Images[1].Parent = strings.Repeat("e", sumHexLen) },
			want:   "images[1].parent: unknown image id",
		},
		{
			name:   "incremental without parent",
			mutate: func(m *Manifest) { m.Images[1].Parent = "" },
			want:   "images[1].parent: incremental nodes must have a parent",
		},
		{
			name: "cycle",
			mutate: func(m *Manifest) {
				m.Images[1].Parent = m.Images[2].ID
			},
			want: "images[1].parent: the parent edges must stay acyclic",
		},
		{
			name: "two full roots",
			mutate: func(m *Manifest) {
				m.Images[1].Kind = KindFull
				m.Images[1].Parent = ""
			},
			want: "images[1].kind: more than one full root",
		},
		{
			name:   "no full root",
			mutate: func(m *Manifest) { m.Images[0].Kind = KindIncremental },
			want:   "images: must hold exactly one full root",
		},
		{
			name:   "root with parent",
			mutate: func(m *Manifest) { m.Images[0].Parent = m.Images[1].ID },
			want:   "images[0].parent: the full root must not have a parent",
		},
		{
			name:   "unknown head",
			mutate: func(m *Manifest) { m.Head = strings.Repeat("d", sumHexLen) },
			want:   "head: unknown image id",
		},
		{
			name:   "empty head",
			mutate: func(m *Manifest) { m.Head = "" },
			want:   "head: must be set",
		},
		{
			name:   "blocks on a single payload layer",
			mutate: func(m *Manifest) { m.Images[1].Blocks = blocksSeq(1) },
			want:   "images[1].blocks: must be empty for single payloads",
		},
		{
			name: "blocks missing on a split payload layer",
			mutate: func(m *Manifest) {
				m.Images[0].Blocks = blocksSeq(2)
			},
			want: "images[1].blocks: must be set for blocks payloads",
		},
		{
			name:   "old version",
			mutate: func(m *Manifest) { m.Version = 2 },
			want:   "version: unsupported metadata version 2",
		},
		{
			name:   "bad name",
			mutate: func(m *Manifest) { m.Name = "alice//app" },
			want:   "name: invalid image name",
		},
		{
			name:   "bad format",
			mutate: func(m *Manifest) { m.Format.Type = "tar" },
			want:   `format.type: unknown container type: "tar"`,
		},
		{
			name:   "invalid kind",
			mutate: func(m *Manifest) { m.Images[1].Kind = "layer" },
			want:   "images[1].kind: must be full or incremental",
		},
		{
			name:   "malformed parent id",
			mutate: func(m *Manifest) { m.Images[1].Parent = "xyz" },
			want: "images[1].parent: must be 64 lowercase hex " +
				"digits, or a legacy 16-digit id",
		},
		{
			name:   "digest mismatch",
			mutate: func(m *Manifest) { m.Images[1].SHA256 = strings.Repeat("9", sumHexLen) },
			want:   "images[1].id: must equal sha256",
		},
		{
			name:   "malformed digest",
			mutate: func(m *Manifest) { m.Images[1].SHA256 = "not-hex" },
			want: "images[1].sha256: must be 64 lowercase hex " +
				"digits, or a legacy 16-digit id",
		},
		{
			name:   "zero size",
			mutate: func(m *Manifest) { m.Images[1].Size = 0 },
			want:   "images[1].size: must be greater than 0",
		},
		{
			name:   "negative mtime",
			mutate: func(m *Manifest) { m.Images[1].MtimeUnix = -1 },
			want:   "images[1].mtime_unix: must not be negative",
		},
		{
			name:   "missing created_at",
			mutate: func(m *Manifest) { m.Images[1].CreatedAt = time.Time{} },
			want:   "images[1].created_at: must be set",
		},
		{
			name: "non-UTC created_at",
			mutate: func(m *Manifest) {
				m.Images[1].CreatedAt = time.Date(2026, 10, 16, 9, 0, 0, 0,
					time.FixedZone("CET", 3600))
			},
			want: "images[1].created_at: must be UTC",
		},
		{
			name:   "root without builder",
			mutate: func(m *Manifest) { m.Images[0].Builder = "" },
			want:   "images[0].builder: must be set",
		},
		{
			name:   "malformed root content digest",
			mutate: func(m *Manifest) { m.Images[0].ContentSHA256 = strings.Repeat("z", sumHexLen) },
			want:   "images[0].content_sha256: must be 64 lowercase hex digits",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifest := validManifest()
			tt.mutate(&manifest)
			err := manifest.Validate()
			if err == nil {
				t.Fatalf("manifest accepted, want %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
	if err := validManifest().Validate(); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
}

func TestManifestValidateEnforcesStackDepth(t *testing.T) {
	atLimit := deepManifest(maxStackDepth - 1)
	if err := atLimit.Validate(); err != nil {
		t.Fatalf("depth %d rejected: %v", maxStackDepth, err)
	}
	stack, err := atLimit.HeadStack()
	if err != nil {
		t.Fatal(err)
	}
	if len(stack) != maxStackDepth {
		t.Fatalf("stack = %d, want %d", len(stack), maxStackDepth)
	}
	over := deepManifest(maxStackDepth)
	err = over.Validate()
	if err == nil {
		t.Fatal("over-deep manifest accepted")
	}
	if !strings.Contains(err.Error(), "exceeds the stack limit of 8") {
		t.Fatalf("error = %v", err)
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("images[%d]:", maxStackDepth)) {
		t.Fatalf("deepest node not named: %v", err)
	}
}

func TestManifestValidateAcceptsLegacyIDs(t *testing.T) {
	full := "0123456789abcdef"
	tip := "fedcba9876543210"
	manifest := Manifest{
		Version: VersionManifestVersion,
		Name:    "alice/app",
		Format:  Format{Type: FormatSquashfs, Compression: CompressionZstd},
		Head:    tip,
		Images: []ImageMeta{
			{
				ID:        full,
				Kind:      KindFull,
				Size:      12345,
				SHA256:    full,
				MtimeUnix: 1710000000,
				CreatedAt: time.Date(2026, 10, 16, 8, 0, 0, 0, time.UTC),
				Builder:   "dev",
			},
			{
				ID:        tip,
				Kind:      KindIncremental,
				Parent:    full,
				Size:      4096,
				SHA256:    tip,
				MtimeUnix: 1710000001,
				CreatedAt: time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC),
			},
		},
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("legacy ids rejected: %v", err)
	}
	stack, err := manifest.HeadStack()
	if err != nil {
		t.Fatal(err)
	}
	if len(stack) != 2 || stack[0].ID != full || stack[1].ID != tip {
		t.Fatalf("head stack = %+v", stack)
	}
	bad := manifest
	bad.Images[1].ID = "0123456789abcde"
	if err := bad.Validate(); err == nil ||
		!strings.Contains(err.Error(), "images[1].id: must be 64 lowercase hex digits") {
		t.Fatalf("short id: %v", err)
	}
}

// validManifestWithBranch is validManifest plus one branch node
// parented on the root. The head stays on the linear tip, so the
// branch node is off the head stack. It returns the manifest and the
// branch node's id.
func validManifestWithBranch() (Manifest, string) {
	manifest := validManifest()
	branch := testID("beefbeefbeefbeef")
	manifest.Images = append(manifest.Images, ImageMeta{
		ID:        branch,
		Kind:      KindIncremental,
		Parent:    manifest.Images[0].ID,
		Size:      4096,
		SHA256:    branch,
		MtimeUnix: 1710000003,
		CreatedAt: time.Date(2026, 10, 16, 11, 0, 0, 0, time.UTC),
	})
	return manifest, branch
}

func TestManifestBranchesCountsOffHeadNodes(t *testing.T) {
	manifest := validManifest()
	if got := manifest.Branches(); got != 0 {
		t.Fatalf("linear manifest branches = %d", got)
	}
	withBranch, branch := validManifestWithBranch()
	if got := withBranch.Branches(); got != 1 {
		t.Fatalf("branches = %d, want 1", got)
	}
	// Moving the head onto the branch leaves the two linear nodes
	// off the head stack.
	reheaded := withBranch
	reheaded.Head = branch
	if got := reheaded.Branches(); got != 2 {
		t.Fatalf("reheaded branches = %d, want 2", got)
	}
}

func blocksSeq(n int) []BlockEntry {
	entries := make([]BlockEntry, n)
	for i := range entries {
		entries[i] = BlockEntry{
			Name:      fmt.Sprintf(blockNameFmt, i),
			Size:      int64(4096 + i),
			SHA256:    strings.Repeat("ab", 32),
			MtimeUnix: int64(1710000000 + i),
		}
	}
	return entries
}
