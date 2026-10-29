package image

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ImportMode is the exclusive import operation. Link records a
// position file. Copy streams a later staging pass. Planning does
// not perform either write.
type ImportMode string

const (
	// ImportLink plans a read-only position import.
	ImportLink ImportMode = "link"
	// ImportCopy plans a later streamed copy. The digest stays empty
	// until that pass hashes the bytes it writes.
	ImportCopy ImportMode = "copy"
)

// SourceKind is the caller input after stat: one container file or
// one image directory.
type SourceKind string

const (
	// SourceFile is one regular container, not a location file.
	SourceFile SourceKind = "file"
	// SourceDir is a directory whose meta.yaml is current v3.
	SourceDir SourceKind = "dir"
)

// ImportRequest is one import. Hash is a directory selector owned by
// a later step; a file source rejects any non-empty value. Planning
// does not create the target repository.
type ImportRequest struct {
	Mode      ImportMode
	ImagesDir string
	Name      string
	Source    string
	Hash      string
}

// SourcePlan is a read-only description of one source. Given is the
// absolute caller path and is the sidecar key. Location is that path
// with symlinks resolved, used for identity and overlap checks. For a
// directory, Size and MtimeUnix describe meta.yaml. Link file plans
// set SHA256 from the sidecar, or UnknownSHA256 when it is absent,
// and do not hash the payload.
type SourcePlan struct {
	Mode      ImportMode
	Kind      SourceKind
	Name      string
	Repo      Repo
	Given     string
	Location  string
	Format    Format
	Size      int64
	MtimeUnix int64
	SHA256    string
}

// PlanImportSource classifies and canonicalizes req.Source. It reads
// metadata, sidecars, and bounded container probes only. It does not
// lock or write the source, write an index, or hash link payloads.
func PlanImportSource(req ImportRequest) (SourcePlan, error) {
	if req.Mode != ImportLink && req.Mode != ImportCopy {
		return SourcePlan{}, importFail(req, fmt.Errorf("mode %q must be link or copy", req.Mode))
	}
	if strings.TrimSpace(req.Source) == "" {
		return SourcePlan{}, importFail(req, errors.New("source path is required"))
	}
	repo, err := OpenRepo(req.ImagesDir, req.Name)
	if err != nil {
		return SourcePlan{}, importFail(req, err)
	}
	if err := checkNamePlacement(req.ImagesDir, repo.Segments); err != nil {
		return SourcePlan{}, importFail(req, err)
	}
	return planImport(req, repo)
}

func planImport(req ImportRequest, repo Repo) (SourcePlan, error) {
	given, err := filepath.Abs(req.Source)
	if err != nil {
		return SourcePlan{}, importFail(req, err)
	}
	given = filepath.Clean(given)
	info, err := os.Stat(given)
	if err != nil {
		return SourcePlan{}, importFail(req, err)
	}
	plan := SourcePlan{Mode: req.Mode, Name: repo.Name(), Repo: repo, Given: given}
	switch {
	case info.IsDir():
		return planDirectory(req, plan)
	case info.Mode().IsRegular():
		return planFile(req, plan)
	default:
		return SourcePlan{}, importFail(req, fmt.Errorf("%s is not a file or directory", given))
	}
}

func planDirectory(req ImportRequest, plan SourcePlan) (SourcePlan, error) {
	loc, err := canonicalPath(plan.Given)
	if err != nil {
		return SourcePlan{}, importFail(req, err)
	}
	if err := validateImportLocation(loc, plan.Repo.Dir); err != nil {
		return SourcePlan{}, importFail(req, err)
	}
	meta := filepath.Join(loc, fileMeta)
	info, err := os.Stat(meta)
	if err != nil {
		return SourcePlan{}, importFail(req, fmt.Errorf("metadata: %w", err))
	}
	if !info.Mode().IsRegular() {
		return SourcePlan{}, importFail(req, errors.New("metadata is not a regular file"))
	}
	body, err := os.ReadFile(meta)
	if err != nil {
		return SourcePlan{}, importFail(req, fmt.Errorf("metadata: %w", err))
	}
	if _, err := DecodeVersionManifest(body); err != nil {
		return SourcePlan{}, importFail(req, err)
	}
	plan.Kind = SourceDir
	plan.Location = loc
	plan.Size = info.Size()
	plan.MtimeUnix = info.ModTime().Unix()
	return plan, nil
}

func planFile(req ImportRequest, plan SourcePlan) (SourcePlan, error) {
	if req.Hash != "" {
		return SourcePlan{}, importFail(req, errors.New("hash selector requires an image directory"))
	}
	loc, info, err := canonicalFile(plan.Given)
	if err != nil {
		return SourcePlan{}, importFail(req, err)
	}
	if err := validateImportLocation(loc, plan.Repo.Dir); err != nil {
		return SourcePlan{}, importFail(req, err)
	}
	if isBlockName(filepath.Base(loc)) {
		return SourcePlan{}, importFail(req, fmt.Errorf("%s is a block fragment", loc))
	}
	if err := rejectLocationChain(loc); err != nil {
		return SourcePlan{}, importFail(req, err)
	}
	format, err := probeImportFile(loc)
	if err != nil {
		if locErr := rejectDanglingLocation(loc, info.Size()); locErr != nil {
			return SourcePlan{}, importFail(req, locErr)
		}
		return SourcePlan{}, importFail(req, err)
	}
	plan.Kind = SourceFile
	plan.Location = loc
	plan.Format = format
	plan.Size = info.Size()
	plan.MtimeUnix = info.ModTime().Unix()
	if req.Mode != ImportLink {
		return plan, nil
	}
	sum, err := ReadChecksumSidecar(plan.Given)
	if err != nil {
		return SourcePlan{}, importFail(req, err)
	}
	plan.SHA256 = sum
	return plan, nil
}

func canonicalFile(path string) (string, os.FileInfo, error) {
	loc, err := canonicalPath(path)
	if err != nil {
		return "", nil, err
	}
	info, err := os.Stat(loc)
	if err != nil {
		return "", nil, err
	}
	if !info.Mode().IsRegular() {
		return "", nil, fmt.Errorf("%s is not a regular file", loc)
	}
	return loc, info, nil
}

func probeImportFile(path string) (Format, error) {
	typ, comp, err := detectLinkFormat(path)
	if err != nil {
		return Format{}, err
	}
	if err := refuseLinkFormat(typ); err != nil {
		return Format{}, err
	}
	return Format{Type: typ, Compression: comp}, nil
}

func canonicalPath(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}

func validateImportLocation(loc, repoDir string) error {
	if loc == "" || strings.ContainsAny(loc, "\n\r\x00") || len(loc) >= pathRefMaxSize {
		return fmt.Errorf("invalid location %q", loc)
	}
	root, err := canonicalRoot(repoDir)
	if err != nil {
		return err
	}
	if sameOrNested(loc, root) {
		return fmt.Errorf("source overlaps target %s", root)
	}
	return nil
}

func canonicalRoot(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	cur := filepath.Clean(abs)
	var missing []string
	for {
		resolved, err := filepath.EvalSymlinks(cur)
		if err == nil {
			parts := append([]string{resolved}, missing...)
			return filepath.Clean(filepath.Join(parts...)), nil
		}
		parent := filepath.Dir(cur)
		if !os.IsNotExist(err) || parent == cur {
			return "", err
		}
		missing = append([]string{filepath.Base(cur)}, missing...)
		cur = parent
	}
}

// sameOrNested reports whether a and b are the same path or one
// contains the other. filepath.Rel treats the filesystem root as a
// parent; a separator prefix does not, because Clean("/") plus "/"
// is "//".
func sameOrNested(a, b string) bool {
	return pathInside(a, b) || pathInside(b, a)
}

func pathInside(path, root string) bool {
	path = filepath.Clean(path)
	root = filepath.Clean(root)
	if path == root {
		return true
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func rejectLocationChain(start string) error {
	target, ok, err := readExternalLinkTarget(start)
	if err != nil || !ok {
		return err
	}
	seen := map[string]struct{}{start: {}}
	for range 8 {
		if _, loop := seen[target]; loop {
			return fmt.Errorf("location file cycle at %s", start)
		}
		seen[target] = struct{}{}
		next, nextOK, err := readExternalLinkTarget(target)
		if err != nil {
			return err
		}
		if !nextOK {
			return fmt.Errorf("location file %s", start)
		}
		target = next
	}
	return fmt.Errorf("recursive location file %s", start)
}

func rejectDanglingLocation(path string, size int64) error {
	if size <= 0 || size > pathRefMaxSize {
		return nil
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	line, ok := absoluteLocationLine(body)
	if !ok {
		return nil
	}
	info, err := os.Stat(line)
	if err != nil {
		return fmt.Errorf("dangling location file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("location file points at %s", line)
	}
	return nil
}

func absoluteLocationLine(body []byte) (string, bool) {
	if len(body) == 0 || bytes.IndexByte(body, 0) >= 0 {
		return "", false
	}
	line := strings.TrimSpace(string(body))
	if line == "" || strings.ContainsAny(line, "\r\n") || !filepath.IsAbs(line) {
		return "", false
	}
	return filepath.Clean(line), true
}

func importFail(req ImportRequest, err error) error {
	return fmt.Errorf("import %s: image %s: source %s: %w", req.Mode, req.Name, req.Source, err)
}
