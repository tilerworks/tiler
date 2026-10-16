// Package cli defines the tiler command tree.
package cli

// RootCmd is the process entry command. Later steps attach
// subcommands in this package so the command tree has one definition.
type RootCmd struct {
	Use string
}

// NewRootCmd returns the root command. Its use line is the binary name.
func NewRootCmd() *RootCmd {
	return &RootCmd{Use: "tiler"}
}
