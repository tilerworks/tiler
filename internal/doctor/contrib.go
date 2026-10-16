package doctor

const (
	nameContribConcatfs = "contrib_concatfs"
	nameContribSlowfs   = "contrib_slowfs"

	// contribGuidanceText explains where the optional helper tools
	// come from and that tiler never installs them itself. It
	// describes the design points in words and never cites external
	// document numbers.
	contribGuidanceText = "optional tool; C sources ship in the contrib/ " +
		"directory of the tiler source tree with origin, license, and " +
		"build notes; an on-site build needs gcc and libfuse3-dev and " +
		"takes about a second; tiler never installs it automatically"
)

// contribTools are the optional helper tools: concatfs mounts a
// byte-chunk concatenated view, slowfs injects latency for load
// tests. Both are advisory — a missing tool never fails doctor.
var contribTools = []struct {
	name   string
	binary string
}{
	{name: nameContribConcatfs, binary: "concatfs"},
	{name: nameContribSlowfs, binary: "slowfs"},
}

// probeContrib reports each optional helper tool through the
// LookPath seam: found is a pass carrying the resolved path, and
// missing is a warn with the install guidance.
func probeContrib(seams Seams) []Result {
	results := make([]Result, 0, len(contribTools))
	for _, tool := range contribTools {
		path, err := seams.LookPath(tool.binary)
		if err != nil {
			results = append(results, warn(tool.name, contribGuidanceText))
			continue
		}
		result := pass(tool.name, path)
		result.ToolPath = path
		results = append(results, result)
	}
	return results
}
