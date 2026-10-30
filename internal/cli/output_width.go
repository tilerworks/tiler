package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

const (
	defaultOutputWidth = 120
	minimumOutputWidth = 80
)

func bindOutputWidth(cmd *cobra.Command, width *int) {
	cmd.Flags().IntVarP(width, "width", "w", defaultOutputWidth,
		"text output width in columns (minimum 80; ignored with --json)")
}

func validateOutputWidth(width int) error {
	if width < minimumOutputWidth {
		return usageError{fmt.Errorf("width must be at least %d columns", minimumOutputWidth)}
	}
	return nil
}
