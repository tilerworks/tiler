package image

import (
	"fmt"
	"strings"
	"testing"
)

func TestValidateSingleChainRejectsBlocks(t *testing.T) {
	meta := validMeta()
	entry := validEntry(meta.ID, "fedcba9876543210")
	entry.Blocks = blocksSeq(1)
	meta.Chain = []ChainEntry{entry}
	assertIssues(t, meta, []string{"chain[0].blocks: must be empty for single payloads"})
}

func TestValidateBlocksManifestRules(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Meta)
		wants  []string
	}{
		{
			name:   "valid blocks manifest",
			mutate: func(*Meta) {},
		},
		{
			name: "members form is refused",
			mutate: func(m *Meta) {
				m.Members = []MemberEntry{{Name: "alpha"}}
			},
			wants: []string{"members: the members split form is no longer supported"},
		},
		{
			name:   "incremental kind is refused",
			mutate: func(m *Meta) { m.Kind = KindIncremental },
			wants:  []string{"kind: must be full for split payloads"},
		},
		{
			name: "chain with blocks is valid",
			mutate: func(m *Meta) {
				entry := validEntry(m.ID, "fedcba9876543210")
				entry.Blocks = blocksSeq(1)
				m.Chain = []ChainEntry{entry}
			},
		},
		{
			name: "chain entry missing blocks",
			mutate: func(m *Meta) {
				m.Chain = []ChainEntry{validEntry(m.ID, "fedcba9876543210")}
			},
			wants: []string{"chain[0].blocks: must be set for blocks payloads"},
		},
		{
			name:   "foreign name format",
			mutate: func(m *Meta) { m.Blocks[0].Name = "part.000" },
			wants:  []string{"blocks[0].name: must be blk.000"},
		},
		{
			name:   "duplicate name breaks the sequence",
			mutate: func(m *Meta) { m.Blocks[0].Name = "blk.001" },
			wants:  []string{"blocks[0].name: must be blk.000"},
		},
		{
			name: "out of order names",
			mutate: func(m *Meta) {
				m.Blocks[0].Name, m.Blocks[1].Name = "blk.001", "blk.000"
			},
			wants: []string{
				"blocks[0].name: must be blk.000",
				"blocks[1].name: must be blk.001",
			},
		},
		{
			name:   "zero block size",
			mutate: func(m *Meta) { m.Blocks[1].Size = 0 },
			wants:  []string{"blocks[1].size: must be greater than 0"},
		},
		{
			name:   "uppercase block digest",
			mutate: func(m *Meta) { m.Blocks[0].SHA256 = strings.Repeat("AB", 32) },
			wants:  []string{"blocks[0].sha256: must be 64 lowercase hex digits"},
		},
		{
			name:   "short block digest",
			mutate: func(m *Meta) { m.Blocks[0].SHA256 = "abcd" },
			wants:  []string{"blocks[0].sha256: must be 64 lowercase hex digits"},
		},
		{
			name:   "manifest at capacity is valid",
			mutate: func(m *Meta) { m.Blocks = blocksSeq(maxBlocks) },
		},
		{
			name:   "manifest over capacity",
			mutate: func(m *Meta) { m.Blocks = blocksSeq(maxBlocks + 1) },
			wants:  []string{"blocks: must hold at most 999 entries"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			meta := validBlocksMeta()
			tc.mutate(&meta)
			assertIssues(t, meta, tc.wants)
		})
	}
}

func TestValidateMembersFormIsRefused(t *testing.T) {
	meta := validMeta()
	meta.Members = []MemberEntry{{
		Name: "alpha", Image: "members/alpha.sqsh", Size: 1,
		SHA256: strings.Repeat("0a", 32),
	}}
	assertIssues(t, meta, []string{
		"members: the members split form is no longer supported",
	})
}

func TestPayloadKindDispatch(t *testing.T) {
	cases := []struct {
		meta Meta
		want string
	}{
		{validMeta(), PayloadSingle},
		{validBlocksMeta(), PayloadBlocks},
	}
	for _, tc := range cases {
		if got := PayloadKind(tc.meta); got != tc.want {
			t.Errorf("PayloadKind = %s, want %s", got, tc.want)
		}
	}
}

func TestTempPayloadDirNamesFollowTempPattern(t *testing.T) {
	first, err := tempBlocksDirName()
	if err != nil {
		t.Fatal(err)
	}
	const prefix = ".blocks.tmp."
	if !strings.HasPrefix(first, prefix) {
		t.Fatalf("name = %s, want prefix %s", first, prefix)
	}
	second, err := tempBlocksDirName()
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("temp names collide: %s", first)
	}
}

// assertIssues validates meta and requires every wanted message. An
// empty wants slice requires a valid meta.
func assertIssues(t *testing.T, meta Meta, wants []string) {
	t.Helper()
	err := meta.Validate()
	if len(wants) == 0 {
		if err != nil {
			t.Fatalf("expected a valid meta: %v", err)
		}
		return
	}
	if err == nil {
		t.Fatal("expected violations")
	}
	for _, want := range wants {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %s", want, err)
		}
	}
}

func validBlocksMeta() Meta {
	meta := validMeta()
	meta.Blocks = blocksSeq(2)
	return meta
}

// blocksSeq is a valid in-sequence manifest of n byte blocks.
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
