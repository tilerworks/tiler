package workspace

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/jiangxin/tiler/internal/container"
)

const dmgShadowName = "upper.shadow"

// hdiutilRun executes one hdiutil constructor vector and returns
// stdout. A non-nil consErr must not start a process.
type hdiutilRun func(argv []string, consErr error) ([]byte, error)

// mountDMG attaches one disk image. It does not build a FUSE stack,
// create upper/ or work/, or copy a shadow from the image library
// or another session. A missing shadow path is still passed to
// attach. The same image and shadow at this directory are reused.
func mountDMG(req Request, seam hooks) (*Workspace, error) {
	if req.Full == "" || req.MergedDir == "" {
		return nil, errors.New("workspace: full image and merged dir are required")
	}
	if !req.ReadOnly && req.RuntimeDir == "" {
		return nil, errors.New("workspace: dmg writable mount needs a runtime dir")
	}
	if len(req.Layers) > 0 || req.Subdir != "" || req.Concat != nil {
		return nil, errors.New("workspace: dmg does not stack layers or expose a subdir")
	}
	if err := dmgNesting(req); err != nil {
		return nil, err
	}

	run := req.Hdiutil
	if run == nil {
		run = execHdiutil
	}
	image := req.Full
	if req.FullFSName != "" {
		image = req.FullFSName
	}
	shadow := ""
	if !req.ReadOnly {
		shadow = filepath.Join(req.RuntimeDir, dmgShadowName)
	}
	w := &Workspace{MergedDir: req.MergedDir, dmg: true, hdiutil: run}
	if req.RuntimeDir != "" {
		w.runtimeDir = filepath.Clean(req.RuntimeDir)
	}
	reused, err := w.dmgAlready(req, image, shadow, seam)
	if err != nil {
		return nil, err
	}
	if reused {
		w.Reused = true
		return w, nil
	}
	if !req.ReadOnly {
		w.UpperDir = shadow
	}
	if err := w.prepareDMGTarget(req); err != nil {
		return nil, err
	}
	argv, consErr := dmgAttachArgv(req, shadow)
	if _, err := callHdiutil(run, argv, consErr); err != nil {
		return nil, w.fail(err)
	}
	w.mounts = []string{req.MergedDir}
	return w, nil
}

func dmgNesting(req Request) error {
	if req.RuntimeDir == "" {
		return nil
	}
	if underPrefix(req.MergedDir, req.RuntimeDir) {
		return fmt.Errorf("workspace: %s is inside the runtime tree %s",
			req.MergedDir, req.RuntimeDir)
	}
	if underPrefix(req.RuntimeDir, req.MergedDir) {
		return fmt.Errorf("workspace: runtime tree %s is inside the mount target %s",
			req.RuntimeDir, req.MergedDir)
	}
	return nil
}

func dmgAttachArgv(req Request, shadow string) ([]string, error) {
	if req.ReadOnly {
		return container.HdiutilAttachReadOnlyArgv(req.Full, req.MergedDir)
	}
	return container.HdiutilAttachWritableArgv(req.Full, req.MergedDir, shadow)
}

// prepareDMGTarget creates the mount point and, for a writable
// view, the runtime directory. It does not create upper.shadow.
func (w *Workspace) prepareDMGTarget(req Request) error {
	info, err := os.Stat(req.MergedDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("workspace: %w", err)
	}
	if err != nil {
		if err := w.mkdirNew(req.MergedDir); err != nil {
			return err
		}
	} else if !info.IsDir() {
		return fmt.Errorf("workspace: %s is not a directory", req.MergedDir)
	}
	if err := guardTarget(req.MergedDir); err != nil {
		return w.fail(err)
	}
	if req.ReadOnly {
		return nil
	}
	if err := w.mkdirNew(req.RuntimeDir); err != nil {
		return w.fail(err)
	}
	return nil
}

// dmgAlready reports whether the target already serves this image.
// A live row with another image or shadow conflicts and does not
// attach. A missing table or a dead row is not a reuse.
func (w *Workspace) dmgAlready(req Request, image, shadow string, seam hooks) (bool, error) {
	if req.Table == nil {
		return false, nil
	}
	entry, listed := findEntry(req.Table, req.MergedDir)
	if !listed || !servingOf(seam, req.MergedDir) {
		return false, nil
	}
	if err := dmgIdentity(req, image, shadow, entry); err != nil {
		return false, err
	}
	if entry.Upper != "" {
		w.UpperDir = entry.Upper
	}
	return true, nil
}

// dmgIdentity reuses the read-only mount-table check. A writable
// disk image matches the image path and this session's shadow.
func dmgIdentity(req Request, image, shadow string, entry Entry) error {
	if entry.Source != "" {
		entry.Source = filepath.Clean(entry.Source)
	}
	if entry.Upper != "" {
		entry.Upper = filepath.Clean(entry.Upper)
	}
	if req.ReadOnly {
		members := []member{{
			path:   req.Full,
			fsname: filepath.Clean(image),
			format: req.Format,
		}}
		return identity(req, members, entry)
	}
	if entry.Source == filepath.Clean(image) &&
		entry.Upper == filepath.Clean(shadow) &&
		len(entry.Lowers) == 0 {
		return nil
	}
	return conflictErr(req.MergedDir, entry)
}

func callHdiutil(run hdiutilRun, argv []string, consErr error) ([]byte, error) {
	if consErr != nil {
		return nil, consErr
	}
	if run == nil {
		run = execHdiutil
	}
	return run(argv, nil)
}

// detachDMGPoint runs hdiutil detach with no -force. A detach
// failure leaves the view mounted. After detach succeeds, info must
// show the mountpoint gone before the caller may mark it unmounted.
// The shadow file is never removed.
func detachDMGPoint(point string, run hdiutilRun) error {
	argv, consErr := container.HdiutilDetachArgv(point)
	if _, err := callHdiutil(run, argv, consErr); err != nil {
		return fmt.Errorf("workspace: detach %s: %w", point, err)
	}
	out, err := callHdiutil(run, container.HdiutilInfoArgv(), nil)
	if err != nil {
		return fmt.Errorf("workspace: detach %s: %w", point, err)
	}
	if dmgInfoLists(out, point) {
		return fmt.Errorf("workspace: %s is still mounted after detach", point)
	}
	return nil
}

// dmgInfoLists reports whether an hdiutil info plist still names
// point as one complete string value. encoding/xml decodes "&amp;",
// so a path that contains "&" matches the escaped document. A longer
// path, image path, shadow path, or sibling that only contains
// point is a different string and does not match.
func dmgInfoLists(out []byte, point string) bool {
	if point == "" {
		return false
	}
	values, err := plistStringValues(out)
	if err != nil {
		return false
	}
	for _, value := range values {
		if value == point {
			return true
		}
	}
	return false
}

// plistStringValues lists every <string> element's decoded text.
// A fragment of sibling string elements is enough; detach checks
// the mount point before status parses the image dictionary.
func plistStringValues(data []byte) ([]string, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	var values []string
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return values, nil
		}
		if err != nil {
			return nil, err
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "string" {
			continue
		}
		var text string
		if err := dec.DecodeElement(&text, &start); err != nil {
			return nil, err
		}
		values = append(values, text)
	}
}

func (w *Workspace) detachDMG() error {
	if len(w.mounts) == 0 {
		return nil
	}
	if err := detachDMGPoint(w.MergedDir, w.hdiutil); err != nil {
		return err
	}
	w.mounts = nil
	return nil
}

func dmgMountEntry(entry Entry) bool {
	if entry.FSType == "dmg" {
		return true
	}
	return filepath.Base(entry.Upper) == dmgShadowName
}
