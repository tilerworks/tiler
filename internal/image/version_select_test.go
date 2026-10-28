package image

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVersionSelectorSyntax(t *testing.T) {
	for _, ref := range []string{"alice/app:", "alice/app:abc", "alice/app:abcd:ef", "alice/app:xyz1", "alice/app:" + strings.Repeat("a", 65)} {
		if _, _, err := ParseVersionSelector(ref); !errors.Is(err, ErrInvalidHashPrefix) {
			t.Errorf("selector %q: %v", ref, err)
		}
	}
	for _, ref := range []string{"", ":abcd", "_bad:abcd"} {
		if _, _, err := ParseVersionSelector(ref); !errors.Is(err, ErrInvalidName) {
			t.Errorf("selector %q: %v", ref, err)
		}
	}
	name, prefix, err := ParseVersionSelector("alice//app:ABCD")
	if err != nil || name != "alice/app" || prefix != "abcd" {
		t.Fatalf("normalized selection = %q, %q, %v", name, prefix, err)
	}
}

func TestVersionSelectionScopesLatestAndHistoricalStacks(t *testing.T) {
	m := versionFixture()
	for i := range m.Images {
		m.Images[i].CreatedAt = time.Unix(int64(i), 0).UTC()
	}
	latest, root, tip := m.Images[3].ID, m.Images[0].ID, m.Images[2].ID
	for _, ref := range []string{m.Name, m.Name + ":" + strings.ToUpper(latest[:10]), m.Name + ":" + latest} {
		stack, err := m.SelectVersion(ref)
		if err != nil || len(stack) != 1 || stack[0].ID != latest {
			t.Fatalf("select %q: %v, %v", ref, stack, err)
		}
	}
	stack, err := m.SelectVersion(m.Name + ":" + tip[:4])
	if err != nil || len(stack) != 3 || stack[0].ID != root || stack[2].ID != tip {
		t.Fatalf("historical stack: %v, %v", stack, err)
	}
	m.Images[3].CreatedAt = m.Images[2].CreatedAt
	m.Images[0], m.Images[2] = m.Images[2], m.Images[0]
	stack, err = m.SelectVersion(m.Name)
	if err != nil || len(stack) != 3 || stack[2].ID != tip {
		t.Fatalf("incremental latest with time tie and unordered metadata: %v, %v", stack, err)
	}
	for ref, message := range map[string]string{m.Name + ":ffff": "no matching image", "other/app": "name does not match"} {
		if _, err := m.SelectVersion(ref); err == nil || !strings.Contains(err.Error(), message) {
			t.Errorf("select %q: %v", ref, err)
		}
	}
	if _, err := (VersionManifest{Version: VersionManifestVersion, Name: m.Name}).SelectVersion(m.Name); err == nil || !strings.Contains(err.Error(), "no image versions") {
		t.Fatalf("empty selection: %v", err)
	}
}

func TestVersionSelectionReportsSortedCompleteAmbiguousIDs(t *testing.T) {
	m := versionFixture()
	a, b := "abcd"+strings.Repeat("1", 60), "abcd"+strings.Repeat("2", 60)
	m.Images[0].ID, m.Images[0].SHA256, m.Images[1].Parent = a, a, a
	m.Images[2].ID, m.Images[2].SHA256 = b, b
	_, err := m.SelectVersion(m.Name + ":ABCD")
	want := "image selector \"" + m.Name + ":ABCD\": ambiguous hash prefix\n  " + a + "\n  " + b + "\nUse a longer hash prefix."
	if err == nil || err.Error() != want {
		t.Fatalf("ambiguous selection: %v", err)
	}
	var ambiguity *AmbiguousVersionError
	if !errors.As(err, &ambiguity) || len(ambiguity.Candidates) != 2 {
		t.Fatalf("missing structured ambiguity: %v", err)
	}
}

func TestMountFilePrecedencePreservesColonFilenames(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("app:abcd", []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"app:abcd", "./app:abcd"} {
		target, err := ResolveMount(buildConfig(t), ref)
		if err != nil || target.Library || len(target.Members) != 1 || filepath.Base(target.Members[0].Path) != "app:abcd" {
			t.Fatalf("file precedence %q: %+v, %v", ref, target, err)
		}
	}
}
