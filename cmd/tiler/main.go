// Command tiler is the process entry. The command tree lives in
// internal/cli.
package main

import (
	"os"

	"github.com/jiangxin/tiler/internal/cli"
	"github.com/jiangxin/tiler/internal/container"
)

func main() {
	// Choose APFS or HFS+ before any image is created. Tests do not
	// call this, so they do not execute hdiutil.
	container.EnableHostDMGFilesystemProbe()
	os.Exit(cli.ExitCode(cli.NewRootCmd().Execute()))
}
