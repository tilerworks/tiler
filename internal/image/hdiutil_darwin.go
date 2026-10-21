//go:build darwin

package image

import (
	"fmt"

	"github.com/jiangxin/tiler/internal/container"
)

// defaultDiskTool runs /usr/bin/hdiutil. Tests pass their own runner
// and do not call this function.
func defaultDiskTool(name string, env []string, args ...string) error {
	if name != "hdiutil" {
		return fmt.Errorf("build: unexpected tool %s", name)
	}
	return runTool(container.HdiutilPath, env, args...)
}
