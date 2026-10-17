package image

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

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
			name:   "both manifests are mutually exclusive",
			mutate: func(m *Meta) { m.Members = validMembers() },
			wants:  []string{"blocks and members: mutually exclusive"},
		},
		{
			name:   "incremental kind is refused",
			mutate: func(m *Meta) { m.Kind = KindIncremental },
			wants:  []string{"kind: must be full for split payloads"},
		},
		{
			name: "chain must stay empty",
			mutate: func(m *Meta) {
				m.Chain = []ChainEntry{validEntry(m.ID, "fedcba9876543210")}
			},
			wants: []string{"chain: must be empty for split payloads"},
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

func TestValidateMembersManifestRules(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Meta)
		wants  []string
	}{
		{
			name:   "valid members manifest",
			mutate: func(*Meta) {},
		},
		{
			name:   "both manifests are mutually exclusive",
			mutate: func(m *Meta) { m.Blocks = validBlocksMeta().Blocks },
			wants:  []string{"blocks and members: mutually exclusive"},
		},
		{
			name:   "incremental kind is refused",
			mutate: func(m *Meta) { m.Kind = KindIncremental },
			wants:  []string{"kind: must be full for split payloads"},
		},
		{
			name: "chain must stay empty",
			mutate: func(m *Meta) {
				m.Chain = []ChainEntry{validEntry(m.ID, "fedcba9876543210")}
			},
			wants: []string{"chain: must be empty for split payloads"},
		},
		{
			name:   "empty name segment",
			mutate: func(m *Meta) { m.Members[0].Name = "" },
			wants:  []string{"members[0].name: invalid segment"},
		},
		{
			name:   "slash in name segment",
			mutate: func(m *Meta) { m.Members[0].Name = "a/b" },
			wants:  []string{"members[0].name: invalid segment"},
		},
		{
			name:   "dot dot name segment",
			mutate: func(m *Meta) { m.Members[0].Name = ".." },
			wants:  []string{"members[0].name: invalid segment"},
		},
		{
			name:   "space in name segment",
			mutate: func(m *Meta) { m.Members[0].Name = "a b" },
			wants:  []string{"members[0].name: invalid segment"},
		},
		{
			name: "duplicate name",
			mutate: func(m *Meta) {
				m.Members[1].Name = "alpha"
				m.Members[1].Image = "members/alpha.sqsh"
			},
			wants: []string{"members[1].name: duplicate"},
		},
		{
			name: "unsorted names",
			mutate: func(m *Meta) {
				m.Members[0], m.Members[1] = m.Members[1], m.Members[0]
			},
			wants: []string{"members[1].name: must be sorted by name"},
		},
		{
			name:   "image path does not follow the name",
			mutate: func(m *Meta) { m.Members[0].Image = "alpha.sqsh" },
			wants:  []string{"members[0].image: must be members/alpha.sqsh"},
		},
		{
			name:   "zero member size",
			mutate: func(m *Meta) { m.Members[0].Size = 0 },
			wants:  []string{"members[0].size: must be greater than 0"},
		},
		{
			name:   "uppercase member digest",
			mutate: func(m *Meta) { m.Members[0].SHA256 = strings.Repeat("0A", 32) },
			wants:  []string{"members[0].sha256: must be 64 lowercase hex digits"},
		},
		{
			name:   "negative member mtime",
			mutate: func(m *Meta) { m.Members[0].MtimeUnix = -1 },
			wants:  []string{"members[0].mtime_unix: must not be negative"},
		},
		{
			name: "whole digest is not the derived manifest digest",
			mutate: func(m *Meta) {
				m.SHA256 = strings.Repeat("ef", 32)
				m.ID = m.SHA256[:idHexLen]
			},
			wants: []string{
				"sha256: must equal the derived members manifest digest",
			},
		},
		{
			name:   "whole size is not the member sum",
			mutate: func(m *Meta) { m.Size++ },
			wants:  []string{"size: must equal the sum of member sizes"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			meta := validMembersMeta()
			tc.mutate(&meta)
			assertIssues(t, meta, tc.wants)
		})
	}
}

func TestPayloadKindDispatch(t *testing.T) {
	cases := []struct {
		meta Meta
		want string
	}{
		{validMeta(), PayloadSingle},
		{validBlocksMeta(), PayloadBlocks},
		{validMembersMeta(), PayloadMembers},
	}
	for _, tc := range cases {
		if got := PayloadKind(tc.meta); got != tc.want {
			t.Errorf("PayloadKind = %s, want %s", got, tc.want)
		}
	}
}

func TestMembersDigestIsCanonicalManifestSHA(t *testing.T) {
	members := validMembers()
	var text string
	for _, entry := range members {
		text += entry.Name + " " +
			strconv.FormatInt(entry.Size, 10) + " " +
			entry.SHA256 + "\n"
	}
	sum := sha256.Sum256([]byte(text))
	want := hex.EncodeToString(sum[:])
	if got := membersDigest(members); got != want {
		t.Fatalf("digest = %s, want %s", got, want)
	}
	swapped := []MemberEntry{members[1], members[0]}
	if got := membersDigest(swapped); got == want {
		t.Fatal("member order does not change the digest")
	}
}

func TestTempPayloadDirNamesFollowTempPattern(t *testing.T) {
	cases := []struct {
		next   func() (string, error)
		prefix string
	}{
		{tempBlocksDirName, ".blocks.tmp."},
		{tempMembersDirName, ".members.tmp."},
	}
	for _, tc := range cases {
		first, err := tc.next()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(first, tc.prefix) {
			t.Fatalf("name = %s, want prefix %s", first, tc.prefix)
		}
		second, err := tc.next()
		if err != nil {
			t.Fatal(err)
		}
		if first == second {
			t.Fatalf("temp names collide: %s", first)
		}
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

func validMembers() []MemberEntry {
	return []MemberEntry{
		{
			Name:      "alpha",
			Image:     "members/alpha.sqsh",
			Size:      3,
			SHA256:    strings.Repeat("0a", 32),
			MtimeUnix: 1710000000,
		},
		{
			Name:      "beta",
			Image:     "members/beta.sqsh",
			Size:      5,
			SHA256:    strings.Repeat("0b", 32),
			MtimeUnix: 1710000001,
		},
	}
}

func validMembersMeta() Meta {
	meta := validMeta()
	meta.Members = validMembers()
	meta.SHA256 = membersDigest(meta.Members)
	meta.ID = meta.SHA256[:idHexLen]
	var total int64
	for _, entry := range meta.Members {
		total += entry.Size
	}
	meta.Size = total
	return meta
}
