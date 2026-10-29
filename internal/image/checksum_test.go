package image

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnknownSHA256IsReservedMarker(t *testing.T) {
	if UnknownSHA256 != strings.Repeat("0", sumHexLen) || checkID(UnknownSHA256) != nil {
		t.Fatalf("UnknownSHA256 = %q", UnknownSHA256)
	}
	empty := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if !IsUnknownSHA256(UnknownSHA256) || IsUnknownSHA256(empty) ||
		IsUnknownSHA256(UnknownSHA256+"0") || IsUnknownSHA256("") {
		t.Fatal("predicate must accept only the reserved marker")
	}
}

func TestParseChecksumSidecarNormalizesOneRecord(t *testing.T) {
	const name = "app.sqsh"
	sum := strings.Repeat("ab", 32)
	upper := strings.ToUpper(sum)
	cases := []struct {
		name    string
		data    string
		want    string
		wantErr string
	}{
		{name: "bare digest", data: sum, want: sum},
		{name: "trailing newline", data: sum + "\n", want: sum},
		{name: "trailing crlf", data: sum + "\r\n", want: sum},
		{name: "uppercase digest", data: upper + "\n", want: sum},
		{name: "sha256sum text", data: sum + "  " + name + "\n", want: sum},
		{name: "sha256sum binary", data: upper + " *" + name, want: sum},
		{name: "explicit zeros", data: UnknownSHA256 + "  " + name + "\n", want: UnknownSHA256},
		{name: "empty", data: "", wantErr: "empty"},
		{name: "newline only", data: "\n", wantErr: "empty"},
		{name: "short", data: "abcd\n", wantErr: "malformed"},
		{name: "bad hex", data: strings.Repeat("g", 64), wantErr: "malformed"},
		{name: "single space", data: sum + " " + name, wantErr: "malformed"},
		{name: "empty name", data: sum + "  \n", wantErr: "malformed"},
		{name: "nul", data: sum + "\x00", wantErr: "malformed"},
		{name: "mismatched name", data: sum + "  other.sqsh\n", wantErr: "mismatched name"},
		{name: "full path name", data: sum + " *" + "/mnt/nas/" + name + "\n", wantErr: "mismatched name"},
		{name: "duplicate records", data: sum + "\n" + sum + "\n", wantErr: "duplicate records"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseChecksumSidecar([]byte(tt.data), name)
			if tt.wantErr != "" {
				if !errors.Is(err, ErrChecksumSidecar) || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %s", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want || (tt.want == UnknownSHA256) != IsUnknownSHA256(got) {
				t.Fatalf("got %s err %v", got, err)
			}
		})
	}
}

func TestParseChecksumSidecarRejectsOversizedInput(t *testing.T) {
	data := bytes.Repeat([]byte("a"), maxChecksumSidecar+1)
	if _, err := ParseChecksumSidecar(data, "app.sqsh"); !errors.Is(err, ErrChecksumSidecar) {
		t.Fatal(err)
	}
}

func TestReadChecksumSidecar(t *testing.T) {
	dir := t.TempDir()
	image := filepath.Join(dir, "app.sqsh")
	payload := []byte("secret-payload")
	if err := os.WriteFile(image, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadChecksumSidecar(image)
	if err != nil || !IsUnknownSHA256(got) {
		t.Fatalf("missing sidecar: got %s err %v", got, err)
	}
	if _, statErr := os.Lstat(image + ".sha256"); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("missing sidecar created a file: %v", statErr)
	}

	sum := strings.Repeat("cd", 32)
	side := image + ".sha256"
	if err := os.WriteFile(side, []byte(strings.ToUpper(sum)+"  app.sqsh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(image, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(image, 0o644) })
	got, err = ReadChecksumSidecar(image)
	if err != nil || got != sum || IsUnknownSHA256(got) {
		t.Fatalf("sidecar digest: got %s err %v", got, err)
	}
	if err := os.Chmod(image, 0o644); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(image)
	if err != nil || !bytes.Equal(body, payload) {
		t.Fatalf("payload changed: %v %q", err, body)
	}

	link := filepath.Join(dir, "link.sqsh")
	if err := os.Symlink(image, link); err != nil {
		t.Fatal(err)
	}
	got, err = ReadChecksumSidecar(link)
	if err != nil || !IsUnknownSHA256(got) {
		t.Fatalf("symlink input must use its own sidecar: got %s err %v", got, err)
	}
	if err := os.Symlink(filepath.Join(dir, "missing"), link+".sha256"); err != nil {
		t.Fatal(err)
	}
	got, err = ReadChecksumSidecar(link)
	if err == nil || got != "" {
		t.Fatalf("dangling sidecar: got %q err %v", got, err)
	}
	blocked := filepath.Join(dir, "blocked.sqsh")
	if err := os.WriteFile(blocked, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blocked+".sha256", []byte(sum+"\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	if f, openErr := os.Open(blocked + ".sha256"); openErr == nil {
		_ = f.Close()
	} else if _, err := ReadChecksumSidecar(blocked); err == nil {
		t.Fatal("unreadable sidecar was accepted")
	}

	if err := os.Remove(link + ".sha256"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(link+".sha256", 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadChecksumSidecar(link); !errors.Is(err, ErrChecksumSidecar) {
		t.Fatalf("directory sidecar: %v", err)
	}

	if err := os.RemoveAll(side); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(side, bytes.Repeat([]byte("a"), maxChecksumSidecar+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadChecksumSidecar(image); !errors.Is(err, ErrChecksumSidecar) {
		t.Fatal(err)
	}
}

func TestReadChecksumSidecarFollowsRegularSymlink(t *testing.T) {
	dir := t.TempDir()
	image := filepath.Join(dir, "app.sqsh")
	if err := os.WriteFile(image, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	sum := strings.Repeat("ab", 32)
	target := filepath.Join(dir, "target.sha256")
	body := sum + "  app.sqsh\n"
	if err := os.WriteFile(target, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, image+".sha256"); err != nil {
		t.Fatal(err)
	}
	got, err := ReadChecksumSidecar(image)
	if err != nil || got != sum {
		t.Fatalf("symlink to regular sidecar: got %s err %v", got, err)
	}
}

// pastBoundReader yields bytes until max, then fails. A scan past
// that bound cannot succeed.
type pastBoundReader struct {
	max int
	n   int
}

func (r *pastBoundReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.n >= r.max || len(p) > r.max-r.n {
		return 0, errors.New("read past bound")
	}
	for i := range p {
		p[i] = 'a'
	}
	r.n += len(p)
	return len(p), nil
}

func TestReadBoundedRejectsReadPastLimit(t *testing.T) {
	const limit = 32
	probe := &pastBoundReader{max: limit + 1}
	data, err := readBounded(probe, limit)
	if data != nil || !errors.Is(err, ErrChecksumSidecar) || probe.n != limit+1 {
		t.Fatalf("data %q err %v read %d", data, err, probe.n)
	}
}
