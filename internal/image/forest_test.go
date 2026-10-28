package image

import (
	"reflect"
	"testing"
	"time"
)

func TestForestOrdersMultipleRootsAndDeepBranches(t *testing.T) {
	node := func(id, kind, parent string, created int64) ImageMeta {
		return ImageMeta{ID: id, Kind: kind, Parent: parent, CreatedAt: time.Unix(created, 0)}
	}
	images := []ImageMeta{
		node("e", KindIncremental, "b", 8), node("d", KindFull, "", 3),
		node("c", KindIncremental, "a", 8), node("a", KindFull, "", 1),
		node("b", KindIncremental, "a", 2), node("f", KindIncremental, "e", 4),
	}
	for i := range images {
		images[i].Blocks = []BlockEntry{{Name: "original"}}
	}
	f, err := NewForest(images)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, n := range f.Topology() {
		ids = append(ids, n.ID)
	}
	if !reflect.DeepEqual(ids, []string{"a", "b", "e", "f", "c", "d"}) {
		t.Fatalf("Topology = %v", ids)
	}
	latest, ok := f.Latest()
	if !ok || latest.ID != "c" {
		t.Fatalf("Latest = %+v, %v", latest, ok)
	}
	latest.Blocks[0].Name = "changed latest"
	stack, err := f.Stack("f")
	if err != nil || len(stack) != 4 || stack[0].ID != "a" || stack[2].ID != "e" {
		t.Fatalf("Stack = %v, %v", stack, err)
	}
	images[3].Parent = "broken"
	images[3].Blocks[0].Name = "changed input"
	order := f.Topology()
	order[0].Parent = "broken"
	order[0].Blocks[0].Name = "changed topology"
	stack[0].Blocks[0].Name = "changed stack"
	if _, err := f.Stack("a"); err != nil {
		t.Fatalf("caller changed forest: %v", err)
	}
	for _, n := range f.Topology() {
		if n.Blocks[0].Name != "original" {
			t.Fatalf("caller changed forest blocks: %+v", n)
		}
	}
}

func TestForestRejectsInvalidEdgesAndAcceptsEmptyGraph(t *testing.T) {
	for _, images := range [][]ImageMeta{
		{{ID: "a", Kind: KindFull, Parent: "a"}},
		{{ID: "a", Kind: KindIncremental}},
		{{ID: "a", Kind: KindIncremental, Parent: "missing"}},
		{{ID: "a", Kind: "other"}}, {{Kind: KindFull}},
		{{ID: "a", Kind: KindFull}, {ID: "a", Kind: KindFull}},
		{{ID: "r", Kind: KindFull}, {ID: "a", Kind: KindIncremental, Parent: "b"},
			{ID: "b", Kind: KindIncremental, Parent: "a"}},
	} {
		if _, err := NewForest(images); err == nil {
			t.Errorf("accepted invalid forest: %+v", images)
		}
	}
	f, err := NewForest(nil)
	if err != nil || len(f.Topology()) != 0 {
		t.Fatalf("empty forest = %+v, %v", f, err)
	}
	if _, ok := f.Latest(); ok {
		t.Fatal("empty forest has a latest node")
	}
	if _, err := f.Stack("missing"); err == nil {
		t.Fatal("accepted unknown tip")
	}
}
