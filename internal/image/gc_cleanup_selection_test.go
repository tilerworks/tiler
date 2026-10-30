package image

import (
	"os"
	"testing"
)

func TestSelectedCleanupDiscoversPartlyRemovedRepository(t *testing.T) {
	root, repo, nodes := removalFixture(t)
	pendingCleanup(t, root, repo, nodes, true)
	if err := os.RemoveAll(repo.Dir); err != nil {
		t.Fatal(err)
	}
	selector, _ := NewNameSelector([]string{repo.Name()})
	plan, names, unmatched, err := InspectSelectedCleanupLocked(root, selector)
	if err != nil || !plan.Pending || len(names) != 1 || names[0] != repo.Name() || len(unmatched) != 0 {
		t.Fatalf("plan=%+v names=%v unmatched=%v err=%v", plan, names, unmatched, err)
	}
	outcomes, err := ExecuteCleanupLocked(root, "", plan)
	if err != nil || len(outcomes) != len(nodes) {
		t.Fatalf("cleanup=%+v err=%v", outcomes, err)
	}
}
