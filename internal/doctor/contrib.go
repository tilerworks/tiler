package doctor

const (
	nameContribConcatfs = "contrib_concatfs"
	nameContribSlowfs   = "contrib_slowfs"

	// contribGuidanceText explains where the optional helper tools
	// come from and that tiler never installs them itself. It
	// describes the design points in words and never cites external
	// document numbers. The wording holds for every contrib tool,
	// whatever its language.
	contribGuidanceText = "optional tool; sources ship in the contrib/ " +
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
		results = append(results, probeContribTool(seams, tool.name, tool.binary))
	}
	return results
}

// probeContribTool reports one optional helper. A resolved path
// passes. A missing binary warns and carries the install guidance.
func probeContribTool(seams Seams, name, binary string) Result {
	path, err := seams.LookPath(binary)
	if err != nil {
		return warn(name, contribGuidanceText)
	}
	result := pass(name, path)
	result.ToolPath = path
	return result
}
