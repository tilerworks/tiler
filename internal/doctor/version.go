package doctor

import (
	"bytes"
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// versionPattern is the required major.minor.patch shape. A shorter
// number is not a version for these probes.
var versionPattern = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

type version struct {
	major int
	minor int
	patch int
	raw   string
}

func (v version) below(major, minor, patch int) bool {
	if v.major != major {
		return v.major < major
	}
	if v.minor != minor {
		return v.minor < minor
	}
	return v.patch < patch
}

// readVersion runs --version and, when that output has no banner,
// -V. A non-zero exit that still prints the tool banner counts as
// success. Context cancellation aborts the whole result set.
func readVersion(ctx context.Context, seams Seams, path string, hints []string) (version, string, error) {
	var lastErr error
	sawOutput := false
	for _, arg := range []string{"--version", "-V"} {
		ver, output, runErr, err := runVersion(ctx, seams, path, arg, hints)
		if err != nil {
			return version{}, "", err
		}
		if ver.raw != "" {
			return ver, "", nil
		}
		if len(bytes.TrimSpace(output)) > 0 {
			sawOutput = true
		}
		if runErr != nil {
			lastErr = runErr
		}
	}
	return version{}, versionFailure(lastErr, sawOutput), nil
}

func runVersion(ctx context.Context, seams Seams, path, arg string, hints []string) (version, []byte, error, error) {
	if err := contextErr(ctx, nil); err != nil {
		return version{}, nil, nil, err
	}
	out, runErr := seams.Run(ctx, path, arg)
	if err := contextErr(ctx, runErr); err != nil {
		return version{}, nil, nil, err
	}
	ver, ok := selectBanner(out, hints)
	if !ok {
		return version{}, out, runErr, nil
	}
	return ver, out, nil, nil
}

func versionFailure(lastErr error, sawOutput bool) string {
	if lastErr != nil && !sawOutput {
		return lastErr.Error()
	}
	if lastErr != nil {
		return "unparsable version: " + lastErr.Error()
	}
	return "unparsable version"
}

func contextErr(ctx context.Context, runErr error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
		return runErr
	}
	return nil
}

// selectBanner keeps the tool's own version line. A line must name
// the tool as its own token. A score of zero never supplies the
// version, so a wrapper line that only contains a version, or a
// substring such as "git" inside "github", is ignored.
func selectBanner(out []byte, hints []string) (version, bool) {
	var best version
	bestScore := 0
	found := false
	for _, line := range strings.Split(string(out), "\n") {
		ver, score, ok := scoreLine(line, hints)
		if !ok || score <= bestScore {
			continue
		}
		best = ver
		bestScore = score
		found = true
	}
	return best, found
}

func scoreLine(line string, hints []string) (version, int, bool) {
	trimmed := strings.TrimSpace(line)
	ver, ok := parseVersion(trimmed)
	if !ok {
		return version{}, 0, false
	}
	return ver, hintScore(trimmed, hints), true
}

func hintScore(line string, hints []string) int {
	tokens := make(map[string]struct{})
	for _, token := range strings.Fields(strings.ToLower(line)) {
		tokens[token] = struct{}{}
	}
	for i, hint := range hints {
		if _, ok := tokens[strings.ToLower(hint)]; ok {
			return len(hints) - i
		}
	}
	return 0
}

func parseVersion(line string) (version, bool) {
	match := versionPattern.FindStringSubmatch(line)
	if match == nil {
		return version{}, false
	}
	major, err1 := strconv.Atoi(match[1])
	minor, err2 := strconv.Atoi(match[2])
	patch, err3 := strconv.Atoi(match[3])
	if err1 != nil || err2 != nil || err3 != nil {
		return version{}, false
	}
	return version{major: major, minor: minor, patch: patch, raw: match[1] + "." + match[2] + "." + match[3]}, true
}
