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
	format, err := container.Lookup(typ)
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
