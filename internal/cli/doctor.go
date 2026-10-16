package cli

import (
	"context"
	"io"

	"github.com/jiangxin/tiler/internal/doctor"
	"github.com/spf13/cobra"
)

// runDoctor produces the probe set. Tests replace it to cover exit
// codes without executing host binaries. The default uses the real
// seams.
var runDoctor = func(ctx context.Context) ([]doctor.Result, error) {
	seams, err := doctor.DefaultSeams()
	if err != nil {
		return nil, err
	}
	return doctor.Probe(ctx, seams)
}

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Probe the local FUSE device and external tools",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return writeDoctor(cmd.Context(), cmd.OutOrStdout())
		},
	}
}

func writeDoctor(ctx context.Context, w io.Writer) error {
	results, err := runDoctor(ctx)
	if err != nil {
		return err
	}
	return doctor.Write(w, results)
}
