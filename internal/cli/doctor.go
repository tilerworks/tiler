package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/jiangxin/tiler/internal/config"
	"github.com/jiangxin/tiler/internal/doctor"
	"github.com/spf13/cobra"
)

// runDoctor produces the probe set from the loaded configuration.
// Tests replace it to cover exit codes without executing host
// binaries. The default uses the real seams.
var runDoctor = func(ctx context.Context, cfg config.Config) ([]doctor.Result, error) {
	seams, err := doctor.DefaultSeams()
	if err != nil {
		return nil, err
	}
	return doctor.Probe(ctx, seams, doctorOptions(cfg))
}

// doctorOptions maps the loaded configuration onto the probe
// options: the storage directories to inspect and the split-storage
// settings that shape probe advice.
func doctorOptions(cfg config.Config) doctor.Options {
	return doctor.Options{
		ImagesDir:    cfg.ImagesDir,
		CacheDir:     cfg.CacheDir,
		SplitEnabled: cfg.SplitEnabled,
		SplitMode:    cfg.SplitMode,
	}
}

// doctorDocument is the --json envelope: one object holding the
// full probe result list.
type doctorDocument struct {
	Results []doctor.Result `json:"results"`
}

func newDoctorCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Probe the local FUSE device and external tools",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, ok := loadedConfig(cmd)
			if !ok {
				return errors.New("doctor: configuration is not loaded")
			}
			return writeDoctor(cmd.Context(), cmd.OutOrStdout(), cfg, asJSON)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "render probe results as a JSON document")
	return cmd
}

// writeDoctor renders the probe report first, human-readable or
// JSON, then applies the verdict: any failed probe becomes a
// runtime error listing the failed names, so doctor exits 1, while
// warn and info results never affect the exit code.
func writeDoctor(ctx context.Context, w io.Writer, cfg config.Config, asJSON bool) error {
	results, err := runDoctor(ctx, cfg)
	if err != nil {
		return err
	}
	if asJSON {
		err = writeJSON(w, doctorDocument{Results: results})
	} else {
		err = doctor.Write(w, results)
	}
	if err != nil {
		return err
	}
	if failed := doctor.Failed(results); len(failed) > 0 {
		return fmt.Errorf("doctor: failing probes: %s", strings.Join(failed, ", "))
	}
	return nil
}
