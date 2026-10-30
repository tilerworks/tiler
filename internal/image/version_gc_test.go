package image

import (
	"errors"
	"os"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/jiangxin/tiler/internal/activity"
)

func seedGCActivity(t *testing.T, repo Repo, node ImageMeta, at time.Time) {
	t.Helper()
	w, err := openVersionLog(repo, node.ID, func() time.Time { return at })
	if err == nil {
		_, err = w.Append(activity.Event{Event: activity.EventMount, Phase: activity.PhaseSucceeded})
	}
	if err != nil {
		t.Fatal(err)
	}
}

func candidateIDs(plan VersionGCPlan) []string {
	ids := make([]string, len(plan.Candidates))
	for i, candidate := range plan.Candidates {
		ids[i] = candidate.Meta.ID
	}
	return ids
}

func TestVersionGCPlansIndependentBranchesAndRetainsAncestors(t *testing.T) {
	for _, retained := range []string{"young child", "referenced child", "damaged child", "equal child", "future child", "young parent", "all expired"} {
		t.Run(retained, func(t *testing.T) {
			repo := testRepo(t)
			base := versionNode("base")
			mid := incrementalNode(base, "mid")
			tip := incrementalNode(mid, "tip")
			sibling, other := incrementalNode(base, "sibling"), versionNode("other")
			nodes := []ImageMeta{base, mid, tip, sibling, other}
			seedVersions(t, repo, nodes...)
			old := testActivityNow.Add(-60 * 24 * time.Hour)
			cutoff := testActivityNow.Add(-30 * 24 * time.Hour)
			for _, node := range nodes {
				at := old
				if node.ID == tip.ID {
					switch retained {
					case "young child":
						at = testActivityNow
					case "equal child":
						at = cutoff
					case "future child":
						at = testActivityNow.Add(time.Hour)
					}
				}
				if retained == "young parent" && node.ID == base.ID {
					at = testActivityNow
				}
				seedGCActivity(t, repo, node, at)
			}
			pins := map[string]bool{tip.ID: retained == "referenced child"}
			if retained == "damaged child" {
				if err := os.WriteFile(activityLogPath(t, repo, tip.ID), []byte("{\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			plan, err := PlanVersionGCLocked(repo, pins, cutoff, activityClock)
			if err != nil {
				t.Fatal(err)
			}
			want := []string{sibling.ID, other.ID}
			if retained == "young parent" || retained == "all expired" {
				want = append(want, tip.ID, mid.ID)
			}
			if retained == "all expired" {
				want = append(want, base.ID)
			}
			got := candidateIDs(plan)
			for i, candidate := range plan.Candidates {
				if reflect.DeepEqual(candidate.Meta, ImageMeta{}) || candidate.Activity.Digest == "" {
					t.Fatal("candidate lost its metadata or activity identity")
				}
				if parent := slices.Index(got, candidate.Meta.Parent); parent >= 0 && parent <= i {
					t.Fatal("parent precedes its child in the removal plan")
				}
			}
			slices.Sort(got)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Fatalf("candidates = %v, want %v", got, want)
			}
			if retained == "damaged child" && (len(plan.Skips) == 0 || plan.Skips[0].Reason != "unreadable") {
				t.Fatalf("damaged log was not reported: %+v", plan.Skips)
			}
		})
	}
}

func TestVersionGCInitializesBeforeSnapshotsRegardlessOfManifestOrder(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(map[bool]string{false: "root first", true: "child first"}[reverse], func(t *testing.T) {
			repo := testRepo(t)
			base := versionNode("base")
			tip := incrementalNode(base, "tip")
			other := versionNode("independent")
			nodes := []ImageMeta{base, tip, other}
			if reverse {
				slices.Reverse(nodes)
			}
			seedVersions(t, repo, nodes...)
			for _, node := range []ImageMeta{base, other} {
				seedGCActivity(t, repo, node, testActivityNow.Add(-time.Hour))
			}
			plan, err := PlanVersionGCLocked(repo, nil, testActivityNow.Add(time.Nanosecond), activityClock)
			if err != nil || !slices.Equal(candidateIDs(plan), []string{other.ID}) {
				t.Fatalf("initialized versions expired: %+v, %v", plan, err)
			}
			before := make(map[string][]byte)
			for _, node := range nodes {
				before[node.ID], err = os.ReadFile(activityLogPath(t, repo, node.ID))
				if err != nil {
					t.Fatal(err)
				}
			}
			plan, err = PlanVersionGCLocked(repo, nil, testActivityNow, activityClock)
			if err != nil || len(plan.Skips) != 0 || !slices.Equal(candidateIDs(plan), []string{other.ID}) {
				t.Fatalf("repeated scan = %+v, %v", plan, err)
			}
			for _, node := range nodes {
				after, err := os.ReadFile(activityLogPath(t, repo, node.ID))
				if err != nil || !slices.Equal(before[node.ID], after) {
					t.Fatalf("repeated scan renewed %s: %v", node.ID, err)
				}
			}
		})
	}
}

func TestVersionGCRetainsPartiallyInitializedChainAndIndependentCandidates(t *testing.T) {
	repo := testRepo(t)
	base := versionNode("base")
	mid := incrementalNode(base, "mid")
	tip := incrementalNode(mid, "tip")
	other := versionNode("other")
	seedVersions(t, repo, base, mid, tip, other)
	for _, node := range []ImageMeta{base, mid, other} {
		seedGCActivity(t, repo, node, testActivityNow.Add(-time.Hour))
	}
	opens := make(map[string]int)
	batch := versionActivityBatch{repo: repo, clock: activityClock,
		open: func(repo Repo, id string, clock activity.Clock) (*activity.Writer, error) {
			opens[id]++
			if id == mid.ID && opens[id] > 1 {
				return nil, errors.New("injected mid-chain write failure")
			}
			return openVersionLog(repo, id, clock)
		}}
	p := versionGCPlanner{repo: repo, cutoff: testActivityNow.Add(time.Nanosecond), clock: activityClock,
		ensure: func(id string) ([]string, error) {
			if id == tip.ID {
				return batch.ensure(id)
			}
			return EnsureVersionActivityLocked(repo, id, activityClock)
		}}
	plan, err := p.plan(nil)
	if err != nil || !slices.Equal(candidateIDs(plan), []string{other.ID}) {
		t.Fatalf("partial initialization plan = %+v, %v", plan, err)
	}
	if len(readVersionActivity(t, repo, base.ID)) != 2 || len(plan.Skips) < 2 {
		t.Fatalf("partial writes or source errors lost: %+v", plan.Skips)
	}
}

func TestVersionGCRejectsInvalidCutoffAndUnknownReferences(t *testing.T) {
	repo := testRepo(t)
	seedVersions(t, repo, versionNode("base"))
	if _, err := PlanVersionGCLocked(repo, nil, time.Time{}, nil); err == nil {
		t.Fatal("zero cutoff accepted")
	}
	if _, err := PlanVersionGCLocked(repo, map[string]bool{versionNode("missing").ID: true}, testActivityNow, nil); err == nil {
		t.Fatal("missing referenced version accepted")
	}
}
