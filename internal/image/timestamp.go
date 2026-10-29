package image

import (
	"time"

	"gopkg.in/yaml.v3"
)

// formatSavedTime is the timestamp written into metadata. It is RFC 3339
// in the local zone, to the second, so a UTC instant in +08:00 is stored
// as 2026-10-29T18:07:56+08:00. A zero time stays the UTC zero instant.
func formatSavedTime(t time.Time) string {
	if t.IsZero() {
		return t.UTC().Format(time.RFC3339)
	}
	return t.Truncate(time.Second).In(time.Local).Format(time.RFC3339)
}

// normalizeCreatedAt stores a loaded timestamp as UTC, truncated to the
// second. An older file may carry a fractional second or a Zulu zone;
// both load, and the fraction is discarded.
func normalizeCreatedAt(t time.Time) time.Time {
	if t.IsZero() {
		return t
	}
	return t.UTC().Truncate(time.Second)
}

func normalizeManifestTimes(images []ImageMeta) {
	for i := range images {
		images[i].CreatedAt = normalizeCreatedAt(images[i].CreatedAt)
	}
}

// createdStamp is one created_at scalar still carrying its original instant.
type createdStamp struct {
	node *yaml.Node
	when time.Time
}

// encodeTimestampDocument writes v as YAML and rewrites every created_at
// into the saved RFC 3339 form. yaml.v3 would otherwise emit UTC
// timestamps with a fractional second.
func encodeTimestampDocument(v any) ([]byte, error) {
	var doc yaml.Node
	if err := doc.Encode(v); err != nil {
		return nil, err
	}
	rewriteSavedTimes(&doc)
	return yaml.Marshal(&doc)
}

func rewriteSavedTimes(n *yaml.Node) {
	var stamps []createdStamp
	collectCreatedAt(n, &stamps)
	assignSavedSeconds(stamps)
}

func collectCreatedAt(n *yaml.Node, out *[]createdStamp) {
	if n == nil {
		return
	}
	switch n.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, child := range n.Content {
			collectCreatedAt(child, out)
		}
	case yaml.MappingNode:
		var created *yaml.Node
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, val := n.Content[i], n.Content[i+1]
			if key.Value == "created_at" && val.Kind == yaml.ScalarNode {
				created = val
				continue
			}
			collectCreatedAt(val, out)
		}
		if created == nil {
			return
		}
		parsed, err := time.Parse(time.RFC3339Nano, created.Value)
		if err != nil {
			return
		}
		*out = append(*out, createdStamp{node: created, when: parsed})
	}
}

// assignSavedSeconds drops fractional seconds without moving a
// timestamp into a different second.
func assignSavedSeconds(stamps []createdStamp) {
	for i := range stamps {
		stamps[i].node.Value = formatSavedTime(stamps[i].when)
	}
}
