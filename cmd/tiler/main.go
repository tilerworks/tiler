// Command tiler is the process entry. The command tree lives in
// internal/cli.
package main

import (
	"os"

	"github.com/jiangxin/tiler/internal/cli"
)

func main() {
	os.Exit(cli.ExitCode(cli.NewRootCmd().Execute()))
}
