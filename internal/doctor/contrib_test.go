package doctor

import (
	"context"
	"strings"
	"testing"
)

func TestProbeContribAvailability(t *testing.T) {
	tests := []struct {
		name          string
		missing       []string
		wantStatuses  map[string]string
		wantToolPaths map[string]string
	}{
		{
			name:    "both tools present pass with the resolved path",
			missing: nil,
			wantStatuses: map[string]string{
				nameContribConcatfs: statusPass,
				nameContribSlowfs:   statusPass,
			},
			wantToolPaths: map[string]string{
				nameContribConcatfs: "/usr/bin/concatfs",
				nameContribSlowfs:   "/usr/bin/slowfs",
			},
		},
		{
			name:    "missing tools warn with install guidance",
			missing: []string{"concatfs", "slowfs"},
			wantStatuses: map[string]string{
				nameContribConcatfs: statusWarn,
				nameContribSlowfs:   statusWarn,
			},
		},
		{
			name:    "one missing tool warns only for itself",
			missing: []string{"slowfs"},
			wantStatuses: map[string]string{
				nameContribConcatfs: statusPass,
				nameContribSlowfs:   statusWarn,
			},
			wantToolPaths: map[string]string{
				nameContribConcatfs: "/usr/bin/concatfs",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host := healthyHost("0.5.0")
			for _, binary := range tt.missing {
				host = host.without(binary)
			}
			got := make(map[string]Result)
			for _, result := range probeContrib(host.seams()) {
				got[result.Name] = result
			}
			if len(got) != len(contribTools) {
				t.Fatalf("results = %v, want one per contrib tool", got)
			}
			for name, want := range tt.wantStatuses {
				if got[name].Status != want {
					t.Fatalf("%s status = %s, want %s (%s)",
						name, got[name].Status, want, got[name].Detail)
				}
			}
			for name, want := range tt.wantToolPaths {
				if got[name].ToolPath != want {
					t.Fatalf("%s ToolPath = %q, want %q", name, got[name].ToolPath, want)
				}
				if got[name].Detail != want {
					t.Fatalf("%s detail = %q, want the resolved path %q",
						name, got[name].Detail, want)
				}
			}
			for _, name := range tt.missing {
				result := got["contrib_"+name]
				assertContribGuidance(t, result.Detail)
				if result.ToolPath != "" {
					t.Fatalf("%s ToolPath = %q, want empty", name, result.ToolPath)
				}
			}
		})
	}
}

func TestProbeAppendsContribResultsLast(t *testing.T) {
	host := healthyHost("0.5.0").without("concatfs")
	results, err := Probe(context.Background(), host.seams(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != len(toolSpecs)+9 {
		t.Fatalf("probe count = %d, want %d", len(results), len(toolSpecs)+9)
	}
	concatfs := results[len(results)-2]
	slowfs := results[len(results)-1]
	if concatfs.Name != nameContribConcatfs || concatfs.Status != statusWarn {
		t.Fatalf("concatfs = %s/%s, want %s/warn",
			concatfs.Name, concatfs.Status, nameContribConcatfs)
	}
	assertContribGuidance(t, concatfs.Detail)
	if slowfs.Name != nameContribSlowfs || slowfs.Status != statusPass {
		t.Fatalf("slowfs = %s/%s, want %s/pass", slowfs.Name, slowfs.Status, nameContribSlowfs)
	}
	if slowfs.ToolPath != "/usr/bin/slowfs" {
		t.Fatalf("slowfs ToolPath = %q", slowfs.ToolPath)
	}
}

func TestContribMissingToolIsNeverAFailure(t *testing.T) {
	host := healthyHost("0.5.0").without("concatfs").without("slowfs")
	results := probeContrib(host.seams())
	if names := Failed(results); len(names) != 0 {
		t.Fatalf("Failed = %v, want no failures for optional tools", names)
	}
}

// assertContribGuidance pins the guidance wording: where the sources
// ship — worded so it holds for every contrib tool — what an on-site
// build needs, and that tiler never installs the tool itself.
func assertContribGuidance(t *testing.T, detail string) {
	t.Helper()
	for _, want := range []string{
		"optional tool",
		"sources ship in the contrib/ directory of the tiler source tree",
		"gcc and libfuse3-dev",
		"tiler never installs it automatically",
	} {
		if !strings.Contains(detail, want) {
			t.Fatalf("guidance = %q, want it to contain %q", detail, want)
		}
	}
	if strings.Contains(detail, "C sources") {
		t.Fatalf("guidance still pins the C language: %q", detail)
	}
}
