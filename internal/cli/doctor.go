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
// options: the storage directories to inspect and the effective
// split_size that shapes single-file limit advice.
func doctorOptions(cfg config.Config) doctor.Options {
	return doctor.Options{
		ImagesDir: cfg.ImagesDir,
		CacheDir:  cfg.CacheDir,
		SplitSize: cfg.SplitSize,
	}
}

// doctorDocument is the --json envelope: the resolved configuration
// summary plus the full probe result list.
type doctorDocument struct {
	Config  *doctor.ConfigSummary `json:"config"`
	Results []doctor.Result       `json:"results"`
}

// configSummary maps the loaded configuration onto the config section
// doctor reports: the user config file location and the core runtime
// directories and log level.
func configSummary(cfg config.Config) doctor.ConfigSummary {
	return doctor.ConfigSummary{
		Path:        cfg.UserFile(),
		ImagesDir:   cfg.ImagesDir,
		SessionsDir: cfg.SessionsDir,
		CacheDir:    cfg.CacheDir,
		LogLevel:    cfg.LogLevel,
	}
}

func newDoctorCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Probe the local image toolchain and host tools",
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

// writeDoctor renders the config section and the probe report first,
// human-readable or JSON, then applies the verdict. A failure to
// produce the probe set — including a platform with no host probes —
// is reported only after that rendering, so the resolved
// configuration stays visible. Any failed probe becomes a runtime
// error listing the failed names, so doctor exits 1, while warn and
// info results never affect the exit code. The config section never
// affects the verdict.
func writeDoctor(ctx context.Context, w io.Writer, cfg config.Config, asJSON bool) error {
	results, probeErr := runDoctor(ctx, cfg)
	summary := configSummary(cfg)
	var err error
	if asJSON {
		err = writeJSON(w, doctorDocument{Config: &summary, Results: results})
	} else {
		if err = doctor.WriteConfig(w, summary); err != nil {
			return err
		}
		err = doctor.Write(w, results)
	}
	if err != nil {
		return err
	}
	if probeErr != nil {
		return probeErr
	}
	if failed := doctor.Failed(results); len(failed) > 0 {
		return fmt.Errorf("doctor: failing probes: %s", strings.Join(failed, ", "))
	}
	return nil
}
