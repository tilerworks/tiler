// Command tiler is the process entry. The command tree lives in
// internal/cli.
package main

import (
	"os"

	"github.com/jiangxin/tiler/internal/cli"
)

func main() {
	cmd := cli.NewRootCmd()
	if cmd == nil || cmd.Use == "" {
		os.Exit(1)
	}
}
