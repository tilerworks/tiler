package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// addDefaultListHint reuses the list runner and flags for a command group.
// Explicit list commands and machine-readable output keep their existing shape.
func addDefaultListHint(cmd *cobra.Command) {
	list := cmd.RunE
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		asJSON, err := cmd.Flags().GetBool("json")
		if err != nil {
			return err
		}
		if !asJSON {
			if _, err := fmt.Fprint(cmd.OutOrStdout(), "💡 Use 'ls' to hide this hint; use '-h' to see more subcommands.\n\n"); err != nil {
				return err
			}
		}
		return list(cmd, args)
	}
}
