package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func hasBlockMembers(members []member) bool {
	for _, m := range members {
		if len(m.blocks) != 0 {
			return true
		}
	}
	return false
}

// Mount all concat views before their container mounts, keeping teardown order
// independent of layer order. The descriptors are small runtime files; their
// absolute paths also handle imported blocks whose payloads are position files.
func (w *Workspace) mountBlockMembers(members []member, seam hooks) ([]member, error) {
	members = append([]member(nil), members...)
	for i := range members {
		m := &members[i]
		if len(m.blocks) == 0 {
			continue
		}
		view := filepath.Base(m.path)
		if !strings.Contains(view, "-concat-") {
			return nil, errors.New("workspace: blocks need a concat descriptor name")
		}
		for _, path := range m.blocks {
			if !filepath.IsAbs(path) || strings.ContainsAny(path, "\r\n") {
				return nil, errors.New("workspace: concat block paths must be absolute single lines")
			}
		}
		data := filepath.Join(w.runtimeDir, fmt.Sprintf("block-data-%d", i))
		point := filepath.Join(w.runtimeDir, fmt.Sprintf("concat-%d", i))
		if err := w.mkdirNew(data); err != nil {
			return nil, err
		}
		root, err := os.OpenRoot(data)
		if err != nil {
			return nil, err
		}
		err = root.WriteFile(view, []byte(strings.Join(m.blocks, "\n")+"\n"), 0o600)
		err = errors.Join(err, root.Close())
		if err != nil {
			return nil, err
		}
		w.created = append(w.created, filepath.Join(data, view))
		if err := w.mkdirNew(point); err != nil {
			return nil, err
		}
		if err := w.run("concatfs", data, point, "-o", "ro"); err != nil {
			return nil, fmt.Errorf("workspace: concatfs: %w", err)
		}
		w.mounts = append(w.mounts, point)
		if err := waitLive(point, seam); err != nil {
			return nil, err
		}
		m.path = filepath.Join(point, view)
	}
	return members, nil
}

func cleanBlockTransits(req Request, run runFunc, seam hooks) error {
	for i := len(req.Table) - 1; i >= 0; i-- {
		point := req.Table[i].Point
		if filepath.Dir(point) == req.RuntimeDir && strings.HasPrefix(filepath.Base(point), "concat-") {
			if err := cleanPoint(point, run, seam); err != nil {
				return err
			}
		}
	}
	return nil
}

func hasBlockTransits(req Request) bool {
	for _, entry := range req.Table {
		if filepath.Dir(entry.Point) == req.RuntimeDir && strings.HasPrefix(filepath.Base(entry.Point), "concat-") {
			return true
		}
	}
	return false
}
