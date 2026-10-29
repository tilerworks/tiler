package cli

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/jiangxin/tiler/internal/container"
)

// toolLookPath resolves an external binary for format inference and
// the build/mount toolchain gate. Tests replace it so command tests
// do not depend on the host toolchain.
var toolLookPath = exec.LookPath

// toolFormatLookup keeps native platform restrictions at the tool gate.
// Tests replace it when simulating a foreign host's registered formats.
var toolFormatLookup = container.Lookup

// erofsHelp is the text of mkfs.erofs --help. Tests replace it so
// compression selection does not depend on how the host was built.
var erofsHelp = readErofsHelp

func readErofsHelp() (string, error) {
	out, err := exec.Command("mkfs.erofs", "--help").CombinedOutput()
	if len(out) == 0 && err != nil {
		return "", err
	}
	return string(out), nil
}

// gateAnyStack refuses the command when no container stack is
// complete. A later check still requires the resolved format's tools.
func gateAnyStack(label string) error {
	if container.AnyComplete(toolLookPath) {
		return nil
	}
	return fmt.Errorf("%s: %w; check the environment with tiler doctor",
		label, container.ErrNoToolchain)
}

// gateFormatTools refuses the command when typ's packer or FUSE
// client is missing, even if another stack is complete.
func gateFormatTools(label, typ string) error {
	format, err := toolFormatLookup(typ)
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	missing := container.MissingTools(format, toolLookPath)
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("%s: %s toolchain is incomplete: missing %s; check the environment with tiler doctor",
		label, format.Type(), strings.Join(missing, ", "))
}
