package benchmark

import (
	"fmt"
	"io"
	"os"
	"time"
)

// Env is the process seam Run uses. Tests inject Command, Host,
// Purge, and Sync so the host never starts image tools or sudo.
// A nil Purge uses the platform best-effort cache drop. A nil Sync
// flushes dirty pages after that drop. Nil writers default to the
// standard streams.
type Env struct {
	Command Command
	Host    Host
	Purge   func() string
	Sync    func()
	Stdout  io.Writer
	Stderr  io.Writer
}

// Run executes a Decision in order: metadata, optional probe,
// optional create, optional cold and warm reads, then cleanup.
// The probe file is fsynced before purge. Purge failures become
// purge=no and do not fail the run. A process sync follows each
// purge. Cleanup always runs, including after a failed attach.
// --format json writes one object only after a successful run.
func Run(d Decision, env Env) error {
	env = env.withDefaults()
	state, err := prepareRun(d)
	if err != nil {
		return err
	}
	defer state.cleanup(logCommand(env.Command, state.logs, detachLogName))
	if d.CreatePath {
		if _, err := fmt.Fprintf(env.Stderr,
			"benchmark: warning: path %s did not exist; created it\n", d.Path); err != nil {
			return fmt.Errorf("benchmark: write warning: %w", err)
		}
	}

	var device deviceInfo
	switch d.Format {
	case FormatJSON:
		device, err = collectDeviceInfo(d.Path)
		if err != nil {
			return err
		}
	case FormatTable, "":
		device, err = collectDeviceInfo(d.Path)
		if err != nil {
			return err
		}
		if err := writeMetadata(d, state.runtime, device, env.Stdout); err != nil {
			return err
		}
	}

	var probe *ProbeStats
	if d.Probe {
		stats, probeErr := runProbe(d, state.bench, env)
		if probeErr != nil {
			return probeErr
		}
		if d.Format == FormatJSON {
			probe = &stats
		}
	}

	rep, err := NewReporter(d.Format, env.Stdout)
	if err != nil {
		return err
	}
	if d.Create {
		if err := runCreate(d, state, env, rep); err != nil {
			return err
		}
	}
	if d.Read {
		if err := runRead(d, state, env, rep); err != nil {
			return err
		}
	}
	if d.Format == FormatJSON {
		return writeJSONReport(d, state.runtime, device, probe, rep.Steps(), env.Stdout)
	}
	return nil
}

func (env Env) withDefaults() Env {
	if env.Stdout == nil {
		env.Stdout = os.Stdout
	}
	if env.Stderr == nil {
		env.Stderr = os.Stderr
	}
	if env.Purge == nil {
		env.Purge = platformPurge
	}
	if env.Sync == nil {
		env.Sync = platformSync
	}
	return env
}

// purgeCaches drops the page cache, then asks the host to sync.
// The sync runs after a failed purge too, matching the script's
// os.sync() after every try_purge. Sync errors are ignored.
func purgeCaches(env Env) string {
	purged := "no"
	if env.Purge != nil {
		purged = env.Purge()
	}
	if env.Sync != nil {
		env.Sync()
	}
	return purged
}

func runProbe(d Decision, bench string, env Env) (ProbeStats, error) {
	stats, err := probeDir(bench, d.ProbeOps, func() string {
		return purgeCaches(env)
	}, d.Format, env.Stdout, env.Stderr)
	if err != nil {
		return ProbeStats{}, err
	}
	if d.Format == FormatTable || d.Format == "" {
		if _, err := fmt.Fprintln(env.Stdout); err != nil {
			return ProbeStats{}, err
		}
	}
	return stats, nil
}

func runCreate(d Decision, state *runState, env Env, rep *Reporter) error {
	src, input, files, err := prepareSource(d, state)
	if err != nil {
		return err
	}
	start := time.Now()
	cmd := logCommand(env.Command, state.logs, packLogName(d.Type))
	if err := Pack(cmd, d.Type, src, state.image, d.Compress); err != nil {
		return err
	}
	elapsed := time.Since(start).Seconds()
	info, err := os.Stat(state.image)
	if err != nil {
		return fmt.Errorf("benchmark: stat image: %w", err)
	}
	out := info.Size()
	return rep.Row(Step{
		Name:      "image_create",
		Seconds:   elapsed,
		Bytes:     &out,
		RateBytes: &input,
		Notes:     createNotes(d, files),
	})
}

// prepareSource generates the random payload tree under the runtime
// dir at state.src, never under the bench dir on the measured volume,
// so pack reads local disk while only the image lands on the volume.
func prepareSource(d Decision, state *runState) (src string, input int64, files int, err error) {
	count, err := WritePayload(state.src, d.Size, d.FileSize)
	if err != nil {
		return "", 0, 0, err
	}
	return state.src, d.Size, count, nil
}

func createNotes(d Decision, files int) string {
	note := fmt.Sprintf("%s %d files", typeLabel(d.Type), files)
	if d.Compress != "" {
		note = fmt.Sprintf("%s %s %d files", typeLabel(d.Type), d.Compress, files)
	}
	if d.Mode == "read" {
		note += " setup"
	}
	return note
}

func typeLabel(typ string) string {
	if typ == "tar" {
		return "tar.gz"
	}
	return typ
}

func runRead(d Decision, state *runState, env Env, rep *Reporter) error {
	if err := ensureImage(state.image); err != nil {
		return err
	}
	if d.Type == "tar" {
		return runTarReads(state.image, env, rep)
	}
	return runMountedReads(d, state, env, rep)
}

func ensureImage(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("benchmark: image %s: %w", path, err)
	}
	if info.IsDir() {
		return fmt.Errorf("benchmark: image %s is a directory", path)
	}
	return nil
}

func runTarReads(image string, env Env, rep *Reporter) error {
	purged := purgeCaches(env)
	if err := reportTreeOrTar(rep, "read_image_1", image, true, "tar.gz purge="+purged); err != nil {
		return err
	}
	return reportTreeOrTar(rep, "read_image_2", image, true, "tar.gz warm")
}

func runMountedReads(d Decision, state *runState, env Env, rep *Reporter) error {
	purged := purgeCaches(env)
	start := time.Now()
	cmd := logCommand(env.Command, state.logs, attachLogName(d.Type))
	mount, err := Attach(cmd, env.Host, d.Type, state.image, state.mount)
	if err != nil {
		return err
	}
	state.mounted = &mount
	if err := rep.Row(Step{
		Name:    "image_attach",
		Seconds: time.Since(start).Seconds(),
		Notes:   "readonly",
	}); err != nil {
		return err
	}
	if err := reportTreeOrTar(rep, "read_image_1", state.mount, false, "purge="+purged); err != nil {
		return err
	}
	if err := reportTreeOrTar(rep, "read_image_2", state.mount, false, "warm"); err != nil {
		return err
	}
	detach := logCommand(env.Command, state.logs, detachLogName)
	if err := Detach(detach, mount, state.mount); err != nil {
		return err
	}
	state.mounted = nil
	return nil
}

func reportTreeOrTar(rep *Reporter, name, path string, tar bool, notes string) error {
	var (
		n       int64
		elapsed time.Duration
		err     error
	)
	if tar {
		n, elapsed, err = ReadTar(path)
	} else {
		n, elapsed, err = ReadTree(path)
	}
	if err != nil {
		return err
	}
	return rep.Row(Step{
		Name:    name,
		Seconds: elapsed.Seconds(),
		Bytes:   &n,
		Notes:   notes,
	})
}
