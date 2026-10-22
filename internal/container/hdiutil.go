package container

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

const (
	hdiutilName    = "hdiutil"
	dmgShadowFile  = "upper.shadow"
	dmgVolnameSep  = "/"
	dmgRuntimeName = "runtime"
)

// DMGShadowPath is the writable UDIF shadow for one mount:
// sessions/<name>/runtime/m<seq>/upper.shadow. The image library
// does not store this file. seq is the mount action sequence.
func DMGShadowPath(sessionsDir, name string, seq int) (string, error) {
	if strings.TrimSpace(sessionsDir) == "" {
		return "", errors.New("container: dmg sessions dir is empty")
	}
	if err := checkDMGSessionName(name); err != nil {
		return "", err
	}
	if seq <= 0 {
		return "", fmt.Errorf("container: dmg runtime sequence %d is not positive", seq)
	}
	return filepath.Join(sessionsDir, name, dmgRuntimeName, "m"+strconv.Itoa(seq), dmgShadowFile), nil
}

// HdiutilCreateArgv builds hdiutil create for a directory source.
// compression ulfo, ulmo, and udro become ULFO, ULMO, and UDRO.
// An empty compression is ULFO. volname is the repo segment of an
// owner/repo name, because a slash cannot be a volume label. The
// filesystem is APFS, or HFS+ for every new image when this OS
// cannot use attach -shadow on an APFS image. That choice is not
// a configuration key.
func HdiutilCreateArgv(src, compression, repo, temp string) ([]string, error) {
	if err := requireDMGPath("source", src); err != nil {
		return nil, err
	}
	if err := requireDMGPath("image", temp); err != nil {
		return nil, err
	}
	format, err := dmgFormatCode(compression, true)
	if err != nil {
		return nil, err
	}
	label, err := dmgVolname(repo)
	if err != nil {
		return nil, err
	}
	return []string{
		hdiutilName, "create",
		"-srcfolder", src,
		"-format", format,
		"-fs", DMGCreateFilesystem(),
		"-volname", label,
		temp,
	}, nil
}

// HdiutilConvertArgv builds hdiutil convert. An empty compression
// keeps the base image format. A set compression may be any of
// ulfo, ulmo, or udro. The shadow stays at its runtime path: the
// vector does not remove or rename it.
func HdiutilConvertArgv(image, compression, base, shadow, temp string) ([]string, error) {
	if err := requireDMGPath("image", image); err != nil {
		return nil, err
	}
	if err := requireDMGPath("image", temp); err != nil {
		return nil, err
	}
	if err := checkDMGShadow(shadow); err != nil {
		return nil, err
	}
	chosen := compression
	if chosen == "" {
		chosen = base
	}
	format, err := dmgFormatCode(chosen, false)
	if err != nil {
		return nil, err
	}
	return []string{
		hdiutilName, "convert", image,
		"-format", format,
		"-shadow", shadow,
		"-o", temp,
	}, nil
}

// HdiutilAttachWritableArgv builds a writable hdiutil attach. The
// shadow path is passed through; attach creates it when missing.
// This vector does not copy a shadow from the library or another
// session.
func HdiutilAttachWritableArgv(image, mountpoint, shadow string) ([]string, error) {
	if err := requireDMGAttach(image, mountpoint); err != nil {
		return nil, err
	}
	if err := checkDMGShadow(shadow); err != nil {
		return nil, err
	}
	return []string{
		hdiutilName, "attach",
		"-nobrowse", "-noautoopen",
		"-mountpoint", mountpoint,
		"-shadow", shadow,
		image,
	}, nil
}

// HdiutilAttachReadOnlyArgv builds a read-only hdiutil attach.
// The vector uses -readonly and omits -shadow.
func HdiutilAttachReadOnlyArgv(image, mountpoint string) ([]string, error) {
	if err := requireDMGAttach(image, mountpoint); err != nil {
		return nil, err
	}
	return []string{
		hdiutilName, "attach",
		"-nobrowse", "-noautoopen",
		"-mountpoint", mountpoint,
		"-readonly",
		image,
	}, nil
}

// HdiutilDetachArgv builds hdiutil detach. The vector has no -force.
func HdiutilDetachArgv(mountpoint string) ([]string, error) {
	if err := requireDMGPath("mount", mountpoint); err != nil {
		return nil, err
	}
	return []string{hdiutilName, "detach", mountpoint}, nil
}

// HdiutilInfoArgv builds hdiutil info -plist. ParseHdiutilInfo reads
// the document; this function only returns the argument vector.
func HdiutilInfoArgv() []string {
	return []string{hdiutilName, "info", "-plist"}
}

// RunHdiutil invokes run with a constructor vector. consErr is
// returned unchanged and run is not called, so a failed constructor
// is never executed. The runner name is hdiutil. The darwin file
// execs that name through PATH; tests pass a recorder.
func RunHdiutil(run ToolRunner, argv []string, consErr error) error {
	args, err := hdiutilArgs(argv, consErr)
	if err != nil {
		return err
	}
	if run == nil {
		return errors.New("container: hdiutil runner is nil")
	}
	return run(hdiutilName, nil, args...)
}

// hdiutilArgs returns the arguments after the hdiutil name.
// A constructor error is returned before any command exists.
func hdiutilArgs(argv []string, consErr error) ([]string, error) {
	if consErr != nil {
		return nil, consErr
	}
	if len(argv) < 2 || argv[0] != hdiutilName {
		return nil, errors.New("container: hdiutil argv is empty")
	}
	return argv[1:], nil
}

func requireDMGAttach(image, mountpoint string) error {
	if err := requireDMGPath("image", image); err != nil {
		return err
	}
	return requireDMGPath("mount", mountpoint)
}

func requireDMGPath(kind, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("container: dmg %s path is empty", kind)
	}
	return nil
}

func dmgFormatCode(compression string, emptyIsDefault bool) (string, error) {
	if compression == "" {
		if !emptyIsDefault {
			return "", errors.New("container: dmg compression is empty")
		}
		compression = dmgComp
	}
	switch compression {
	case "ulfo":
		return "ULFO", nil
	case "ulmo":
		return "ULMO", nil
	case "udro":
		return "UDRO", nil
	default:
		return "", fmt.Errorf("container: dmg compression %q is not allowed", compression)
	}
}

// dmgVolname is the label hdiutil can pass to -volname. An
// owner/repo name contributes its repo segment. Colon and slash
// are not volume labels.
func dmgVolname(repo string) (string, error) {
	label := repo
	if strings.Contains(repo, dmgVolnameSep) {
		owner, rest, _ := strings.Cut(repo, dmgVolnameSep)
		if owner == "" || rest == "" || strings.Contains(rest, dmgVolnameSep) {
			return "", fmt.Errorf("container: dmg volume name %q is not a label", repo)
		}
		label = rest
	}
	if label == "" || strings.ContainsAny(label, `/:`) || badDMGLabelRune(label) {
		return "", fmt.Errorf("container: dmg volume name %q is not a label", repo)
	}
	return label, nil
}

func checkDMGSessionName(name string) error {
	if name == "" || name == "." || name == ".." ||
		strings.ContainsAny(name, `/\`) || badDMGLabelRune(name) {
		return fmt.Errorf("container: dmg session name %q is invalid", name)
	}
	return nil
}

func badDMGLabelRune(name string) bool {
	for _, r := range name {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return true
		}
	}
	return false
}

// checkDMGShadow requires sessions/.../runtime/m<seq>/upper.shadow.
// m0 and a leading zero are not sequence ids.
func checkDMGShadow(shadow string) error {
	if strings.TrimSpace(shadow) == "" {
		return errors.New("container: dmg shadow path is empty")
	}
	cleaned := filepath.Clean(shadow)
	if filepath.Base(cleaned) != dmgShadowFile {
		return fmt.Errorf("container: dmg shadow %q is not upper.shadow", shadow)
	}
	runtime := filepath.Dir(cleaned)
	if !dmgRuntimeID(filepath.Base(runtime)) ||
		filepath.Base(filepath.Dir(runtime)) != dmgRuntimeName {
		return fmt.Errorf("container: dmg shadow %q is not under runtime/m<seq>", shadow)
	}
	return nil
}

func dmgRuntimeID(id string) bool {
	if len(id) < 2 || id[0] != 'm' || id[1] < '1' || id[1] > '9' {
		return false
	}
	for _, c := range id[2:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
