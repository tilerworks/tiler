package workspace

import (
	"fmt"
	"strings"
	"testing"
)

func TestStackUsesConfiguredLayerLimitForBothRequestForms(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		for _, tc := range []struct {
			limit, depth int
			wantError    bool
		}{
			{0, 32, false}, {0, 33, true},
			{1, 1, false}, {1, 2, true},
			{8, 8, false}, {8, 9, true},
			{64, 64, false}, {64, 65, true},
		} {
			t.Run(fmt.Sprintf("explicit=%v/limit=%d/depth=%d", explicit, tc.limit, tc.depth), func(t *testing.T) {
				req := Request{MaxLayers: tc.limit, Full: "/base", Format: "squashfs"}
				for i := 1; i < tc.depth; i++ {
					req.Layers = append(req.Layers, fmt.Sprintf("/layer%d", i))
				}
				if explicit {
					req.Members = []MountMember{{Path: req.Full, Format: req.Format}}
					for _, path := range req.Layers {
						req.Members = append(req.Members, MountMember{Path: path, Format: req.Format})
					}
					req.Full, req.Layers = "", nil
				}
				members, err := stack(req)
				if tc.wantError {
					if err == nil || !strings.Contains(err.Error(), "the limit is") {
						t.Fatalf("over-limit stack: %v", err)
					}
				} else if err != nil || len(members) != tc.depth {
					t.Fatalf("at-limit stack: %d members, %v", len(members), err)
				}
			})
		}
	}
}
