package image

import (
	"fmt"
	"slices"
	"strings"
)

// Forest indexes a validated single-parent graph independently of its storage schema.
type Forest struct {
	nodes    map[string]ImageMeta
	topology []ImageMeta
}

// NewForest accepts multiple full roots and returns an immutable graph snapshot.
func NewForest(images []ImageMeta) (Forest, error) {
	f := Forest{nodes: make(map[string]ImageMeta, len(images))}
	ordered := cloneForestNodes(images)
	for _, node := range ordered {
		if _, duplicate := f.nodes[node.ID]; node.ID == "" || duplicate {
			return Forest{}, fmt.Errorf("forest: empty or duplicate image id %q", node.ID)
		}
		f.nodes[node.ID] = node
	}
	children := make(map[string][]ImageMeta)
	slices.SortFunc(ordered, compareCreated)
	for _, node := range ordered {
		if err := f.checkParent(node); err != nil {
			return Forest{}, err
		}
		children[node.Parent] = append(children[node.Parent], node)
	}
	f.topology = forestOrder(children)
	if len(f.topology) != len(images) {
		return Forest{}, fmt.Errorf("forest: parent edges must stay acyclic")
	}
	return f, nil
}

func (f Forest) checkParent(node ImageMeta) error {
	switch node.Kind {
	case KindFull:
		if node.Parent == "" {
			return nil
		}
	case KindIncremental:
		if _, exists := f.nodes[node.Parent]; node.Parent != "" && exists {
			return nil
		}
	}
	return fmt.Errorf("forest: invalid kind or parent for image %q", node.ID)
}

func forestOrder(children map[string][]ImageMeta) []ImageMeta {
	var order []ImageMeta
	pending := slices.Clone(children[""])
	slices.Reverse(pending)
	for len(pending) > 0 {
		node := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		order = append(order, node)
		for i := len(children[node.ID]) - 1; i >= 0; i-- {
			pending = append(pending, children[node.ID][i])
		}
	}
	return order
}

func (f Forest) newer(node, latest ImageMeta) bool {
	if node.CreatedAt.After(latest.CreatedAt) {
		return true
	}
	if node.CreatedAt.Before(latest.CreatedAt) {
		return false
	}
	switch {
	case f.descendsFrom(node.ID, latest.ID):
		return true
	case f.descendsFrom(latest.ID, node.ID):
		return false
	default:
		return node.ID < latest.ID
	}
}

func (f Forest) descendsFrom(id, ancestor string) bool {
	for id != "" {
		node, ok := f.nodes[id]
		if !ok || node.Parent == "" {
			return false
		}
		if node.Parent == ancestor {
			return true
		}
		id = node.Parent
	}
	return false
}

func compareCreated(a, b ImageMeta) int {
	if order := a.CreatedAt.Compare(b.CreatedAt); order != 0 {
		return order
	}
	return strings.Compare(a.ID, b.ID)
}

// Latest selects by creation time. A descendant wins a tie with its
// ancestor, so a layer built in the same second stays newer after
// timestamps are stored to the second. Otherwise the smallest ID wins.
func (f Forest) Latest() (ImageMeta, bool) {
	var latest ImageMeta
	found := false
	for _, node := range f.topology {
		if !found || f.newer(node, latest) {
			latest, found = node, true
		}
	}
	latest.Blocks = slices.Clone(latest.Blocks)
	return latest, found
}

// Topology returns parents before children with chronological sibling ordering.
func (f Forest) Topology() []ImageMeta { return cloneForestNodes(f.topology) }

// Descendants returns root and every node that depends on it, ordered so
// children can be removed before their parents.
func (f Forest) Descendants(root string) ([]ImageMeta, error) {
	if _, exists := f.nodes[root]; !exists {
		return nil, fmt.Errorf("forest: image %q not found", root)
	}
	included := map[string]bool{root: true}
	var nodes []ImageMeta
	for _, node := range f.topology {
		if node.ID == root || included[node.Parent] {
			included[node.ID] = true
			nodes = append(nodes, node)
		}
	}
	slices.Reverse(nodes)
	return cloneForestNodes(nodes), nil
}

// Stack returns the selected tip's ancestors, starting at its full root.
func (f Forest) Stack(tip string) ([]ImageMeta, error) {
	return walkStack(tip, func(id string) (ImageMeta, bool) {
		node, ok := f.nodes[id]
		node.Blocks = slices.Clone(node.Blocks)
		return node, ok
	})
}

func cloneForestNodes(nodes []ImageMeta) []ImageMeta {
	cloned := slices.Clone(nodes)
	for i := range cloned {
		cloned[i].Blocks = slices.Clone(cloned[i].Blocks)
	}
	return cloned
}
