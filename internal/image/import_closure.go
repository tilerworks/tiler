package image

import (
	"errors"
	"fmt"
	"strings"
)

// importHashAll selects every node. The match is case-sensitive, so
// a hex prefix is never treated as this selector.
const importHashAll = "all"

var errNoImportVersions = errors.New("no image versions")

// SourceSnapshot is the single meta.yaml read for a directory import.
// Body is the exact file. Manifest.Name is the source library name
// and may differ from the target name on Plan.
type SourceSnapshot struct {
	Plan     SourcePlan
	Manifest VersionManifest
	Body     []byte
}

// ClosurePlan is the selected dependency closure in import order.
// Ancestors precede descendants and a shared ancestor appears once.
// Nodes keep the snapshot's facts and block order. Planning does not
// open payload files.
type ClosurePlan struct {
	Snapshot SourceSnapshot
	Nodes    []ImageMeta
}

// PlanImportClosure snapshots a v3 directory and selects its closure.
// An empty hash uses Forest.Latest and that node's ancestor stack.
// A 4 to 64 hex prefix, compared in lowercase, selects one node and
// its ancestors, not its descendants. all selects the topology.
// A missing payload outside the closure cannot fail this call,
// because no payload path is opened. The source is not modified.
func PlanImportClosure(req ImportRequest) (ClosurePlan, error) {
	got, err := readImportSource(req)
	if err != nil {
		return ClosurePlan{}, err
	}
	if got.plan.Kind != SourceDir {
		return ClosurePlan{}, importFail(req, errors.New("version selection requires an image directory"))
	}
	nodes, err := selectImportClosure(got.manifest, req.Hash)
	if err != nil {
		return ClosurePlan{}, importFail(req, err)
	}
	return ClosurePlan{
		Snapshot: SourceSnapshot{Plan: got.plan, Manifest: got.manifest, Body: got.body},
		Nodes:    nodes,
	}, nil
}

func selectImportClosure(manifest VersionManifest, hash string) ([]ImageMeta, error) {
	if err := rejectUndefinedUnknown(manifest.Images); err != nil {
		return nil, err
	}
	forest, err := NewForest(manifest.Images)
	if err != nil {
		return nil, err
	}
	if hash == importHashAll {
		return allImportNodes(forest)
	}
	prefix, err := importHashPrefix(hash)
	if err != nil {
		return nil, err
	}
	if prefix == "" {
		return latestImportStack(forest)
	}
	return selectVersionPrefix(forest, hash, prefix)
}

func allImportNodes(forest Forest) ([]ImageMeta, error) {
	nodes := forest.Topology()
	if len(nodes) == 0 {
		return nil, errNoImportVersions
	}
	return nodes, nil
}

func latestImportStack(forest Forest) ([]ImageMeta, error) {
	latest, ok := forest.Latest()
	if !ok {
		return nil, errNoImportVersions
	}
	return forest.Stack(latest.ID)
}

func importHashPrefix(hash string) (string, error) {
	if hash == "" {
		return "", nil
	}
	prefix := strings.ToLower(hash)
	if len(prefix) < 4 || !isIDRef(prefix) {
		return "", ErrInvalidHashPrefix
	}
	return prefix, nil
}

// rejectUndefinedUnknown allows the reserved digest only on a
// single-file full node. Incremental ids and every block digest stay
// real content addresses, including nodes the selector will skip.
func rejectUndefinedUnknown(nodes []ImageMeta) error {
	for i := range nodes {
		node := &nodes[i]
		split := len(node.Blocks) != 0
		if IsUnknownSHA256(node.SHA256) && (node.Kind != KindFull || split) {
			return fmt.Errorf("unknown digest is not valid for %s", node.ID)
		}
		if err := rejectUnknownBlocks(node); err != nil {
			return err
		}
	}
	return nil
}

func rejectUnknownBlocks(node *ImageMeta) error {
	for _, block := range node.Blocks {
		if IsUnknownSHA256(block.SHA256) {
			return fmt.Errorf("unknown digest is not valid for block %s of %s", block.Name, node.ID)
		}
	}
	return nil
}
