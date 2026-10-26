package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/jiangxin/tiler/internal/benchmark"
	"github.com/spf13/cobra"
)

// runBenchmark executes a planned Decision. Tests replace it so the
// command path never packs or mounts.
var runBenchmark = func(d benchmark.Decision, env benchmark.Env) error {
	return benchmark.Run(d, env)
}

func newBenchmarkCmd() *cobra.Command {
	var (
		readonly   bool
		image      string
		benchDir   string
		runtimeDir string
		mode       string
		typ        string
		size       string
		fileSize   string
		compress   string
		src        string
		probeOps   int
		noProbe    bool
		keep       bool
		format     string
	)
	cmd := &cobra.Command{
		Use:   "benchmark <path>",
		Short: "Measure image pack and read performance on a path",
		Long: "benchmark measures one image size on the filesystem that\n" +
			"holds <path>. It does not read .tiler.yaml or accept root\n" +
			"persistent flags. Paths, type, and compression come only\n" +
			"from this command's flags.",
		Args: exactOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := benchmark.Options{
				Path:        args[0],
				ReadOnly:    readonly,
				Image:       image,
				BenchDir:    benchDir,
				BenchDirSet: cmd.Flags().Changed("bench-dir"),
				RuntimeDir:  runtimeDir,
				Mode:        mode,
				Type:        typ,
				Size:        size,
				FileSize:    fileSize,
				Compress:    compress,
				Src:         src,
				SrcSet:      cmd.Flags().Changed("src"),
				ProbeOps:    probeOps,
				NoProbe:     noProbe,
				Keep:        keep,
				Format:      format,
			}
			return runBenchmarkCmd(cmd, opts)
		},
	}
	cmd.Flags().BoolVar(&readonly, "readonly", false,
		"read an existing image without writing under <path>")
	cmd.Flags().StringVar(&image, "image", "", "existing image file for read-only runs")
	cmd.Flags().StringVar(&benchDir, "bench-dir", benchmark.DefaultBenchDir,
		"relative isolation directory created under <path>")
	cmd.Flags().StringVar(&runtimeDir, "runtime-dir", "",
		"local dir for mount, logs, and payload; must be absent or empty "+
			"and is removed at the end (default: a private temp dir)")
	cmd.Flags().StringVar(&mode, "mode", "both", "write, read, or both")
	cmd.Flags().StringVar(&typ, "type", "",
		"image type: dmg, tar, squashfs, or erofs")
	cmd.Flags().StringVar(&size, "size", "128M", "total payload bytes for this run")
	cmd.Flags().StringVar(&fileSize, "file-size", "4M",
		"bytes per generated payload file")
	cmd.Flags().StringVar(&compress, "compress", "",
		"compression token for the selected type")
	cmd.Flags().StringVar(&src, "src", benchmark.DefaultPayloadDir,
		"relative payload directory created under the runtime dir")
	cmd.Flags().IntVar(&probeOps, "probe-ops", 2000,
		"4 KiB random read and write operations")
	cmd.Flags().BoolVar(&noProbe, "no-probe", false, "skip the filesystem probe")
	cmd.Flags().BoolVar(&keep, "keep", false,
		"keep --bench-dir and --runtime-dir after the run")
	cmd.Flags().StringVar(&format, "format", "table",
		"output format: table, tsv, or json")
	// Omit inherited root persistent flags from help; prepare rejects
	// them, and Long already says they are not accepted.
	cmd.SetUsageTemplate(benchmarkUsageTemplate)
	return cmd
}

func runBenchmarkCmd(cmd *cobra.Command, opts benchmark.Options) error {
	decision, err := benchmark.Plan(opts, hostBenchmarkFS())
	if err != nil {
		return benchmarkUsage(err)
	}
	env := benchmark.Env{
		Command: hostBenchmarkTool,
		Stdout:  cmd.OutOrStdout(),
		Stderr:  cmd.ErrOrStderr(),
	}
	return runBenchmark(decision, env)
}

func benchmarkUsage(err error) error {
	var usage *benchmark.UsageError
	if errors.As(err, &usage) {
		return usageError{err}
	}
	return err
}

func hostBenchmarkFS() benchmark.FS {
	return benchmark.FS{
		Stat:    os.Stat,
		ReadDir: os.ReadDir,
		Access:  hostBenchmarkAccess,
	}
}

func hostBenchmarkTool(name string, env []string, args ...string) ([]byte, error) {
	c := exec.Command(name, args...)
	if len(env) > 0 {
		c.Env = append(os.Environ(), env...)
	}
	out, err := c.CombinedOutput()
	if err != nil {
		if len(out) == 0 {
			return out, fmt.Errorf("%s: %w", name, err)
		}
		return out, fmt.Errorf("%s: %w: %s", name, err, string(out))
	}
	return out, nil
}

func rejectBenchmarkRootFlags(cmd *cobra.Command) error {
	root := cmd.Root().PersistentFlags()
	for _, name := range rootPersistentFlagNames {
		f := root.Lookup(name)
		if f == nil || !f.Changed {
			continue
		}
		return usageError{fmt.Errorf(
			"benchmark does not accept --%s; use local flags only", name)}
	}
	return nil
}

// benchmarkUsageTemplate is usageWithoutAliases without the inherited
// Global Flags block. Root persistent flags stay visible on other
// commands; benchmark refuses them at prepare time.
const benchmarkUsageTemplate = `Usage:{{if .Runnable}}
  {{.UseLine}}{{end}}{{if .HasAvailableSubCommands}}
  {{.CommandPath}} [command]{{end}}{{if .HasExample}}

Examples:
{{.Example}}{{end}}{{if .HasAvailableSubCommands}}{{$cmds := .Commands}}{{if eq (len .Groups) 0}}

Available Commands:{{range $cmds}}{{if (or .IsAvailableCommand (eq .Name "help"))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{else}}{{range $group := .Groups}}

{{.Title}}{{range $cmds}}{{if (and (eq .GroupID $group.ID) (or .IsAvailableCommand (eq .Name "help")))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{end}}{{if not .AllChildCommandsHaveGroup}}

Additional Commands:{{range $cmds}}{{if (and (eq .GroupID "") (or .IsAvailableCommand (eq .Name "help")))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{end}}{{end}}{{end}}{{if .HasAvailableLocalFlags}}

Flags:
{{.LocalFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasHelpSubCommands}}

Additional help topics:{{range .Commands}}{{if .IsAdditionalHelpTopicCommand}}
  {{rpad .CommandPath .CommandPathPadding}} {{.Short}}{{end}}{{end}}{{end}}{{if .HasAvailableSubCommands}}

Use "{{.CommandPath}} [command] --help" for more information about a command.{{end}}
`
