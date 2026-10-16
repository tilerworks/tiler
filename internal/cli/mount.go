package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jiangxin/tiler/internal/config"
	"github.com/jiangxin/tiler/internal/image"
	"github.com/jiangxin/tiler/internal/mountinfo"
	"github.com/jiangxin/tiler/internal/session"
	"github.com/jiangxin/tiler/internal/workspace"
	"github.com/spf13/cobra"
)

// Seams the mount and umount commands run through. Tests replace
// them to inject a fake tool runner, a fake cache, a fake mount
// table, and fake engines; the zero values keep the production
// defaults. The engine seams exist because the engine confirms each
// mount with a live stat probe, which a fake tool runner can never
// satisfy without real FUSE mounts.
var (
	// mountRun runs the FUSE tools inside the engine. Nil keeps the
	// engine default.
	mountRun func(name string, args ...string) error
	// ensureCache copies one image into the cache directory.
	ensureCache = image.EnsureCached
	// mountEngine builds the workspace mount.
	mountEngine = workspace.Mount
	// unmountEngine tears a view down by path.
	unmountEngine = workspace.UnmountPath
)

// mountFlags carries the local flags of one mount invocation.
type mountFlags struct {
	readOnly bool
	subdir   string
	verify   bool
	session  string
}

// sessionEnvVar names an existing session a mount attaches to when
// the command line does not name one.
const sessionEnvVar = "TILER_SESSION_ID"

func newMountCmd() *cobra.Command {
	var flags mountFlags
	cmd := &cobra.Command{
		Use:   "mount <image> <dir>",
		Short: "Mount an image as a workspace",
		Long: "Mount serves <image> at <dir>: a writable merged view by\n" +
			"default, or a read-only view with --read-only. <image> is an\n" +
			"owner/repo library reference or the path of a single squashfs\n" +
			"file; the library reference wins when an argument is both.\n" +
			"Chain layers mount as a cascade, --subdir exposes one image\n" +
			"subdirectory as the mount root, and --verify re-hashes every\n" +
			"recorded digest instead of the default quick check.\n" +
			"\n" +
			"Every mount is recorded write-ahead in a session: --session\n" +
			"names one, the " + sessionEnvVar + " environment variable\n" +
			"binds one, and without either an implicit auto-* session is\n" +
			"created. 'session open' later replays the record.",
		Args: exactTwoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMount(cmd, args, flags)
		},
	}
	cmd.Flags().BoolVar(&flags.readOnly, "read-only", false, "mount without a writable upper")
	cmd.Flags().StringVar(&flags.subdir, "subdir", "", "image subdirectory exposed as the mount root")
	cmd.Flags().BoolVar(&flags.verify, "verify", false, "re-verify every recorded digest with sha256")
	cmd.Flags().StringVar(&flags.session, "session", "", "session that records this mount")
	return cmd
}

func runMount(cmd *cobra.Command, args []string, flags mountFlags) error {
	cfg, ok := loadedConfig(cmd)
	if !ok {
		return errors.New("mount: configuration is not loaded")
	}
	dir, err := filepath.Abs(args[1])
	if err != nil {
		return fmt.Errorf("mount: target: %w", err)
	}
	if err := config.OutsideReserved(dir, cfg); err != nil {
		return usageError{err}
	}
	if err := workspace.CheckSubdir(flags.subdir); err != nil {
		return usageError{err}
	}
	target, err := image.ResolveMount(cfg, args[0])
	if err != nil {
		if isResolveUsage(err) {
			return usageError{err}
		}
		return err
	}
	if flags.verify && !target.Library {
		return usageError{errors.New(
			"mount: --verify needs an owner/repo image; a file path records no digest")}
	}
	if err := verifyMembers(target, flags.verify); err != nil {
		return err
	}
	name, implicit, err := resolveMountSession(flags.session)
	if err != nil {
		return err
	}
	engine := sessionEngine(cfg)
	if implicit {
		if name, err = newImplicitSession(engine); err != nil {
			return err
		}
	}
	action, err := mountAction(args[0], target, dir, flags)
	if err != nil {
		return err
	}
	// The write-ahead record is durable before the engine runs and
	// carries the outcome afterwards, so a crash between the two
	// leaves a replayable pending action.
	result, err := engine.RunMount(name, action,
		func(session.Action) (session.Result, error) {
			return runMountEngine(cfg, target, dir, flags)
		})
	if err != nil {
		return err
	}
	verb := "mounted"
	if result.Reused {
		verb = "already mounted"
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", verb, dir)
	return err
}

// resolveMountSession applies the session priority: the --session
// flag, then the environment variable, then implicit creation. A
// bad flag value is a usage problem; a bad environment value is a
// runtime error naming the variable.
func resolveMountSession(flag string) (string, bool, error) {
	if flag != "" {
		if err := session.ValidateName(flag); err != nil {
			return "", false, usageError{err}
		}
		return flag, false, nil
	}
	if env := os.Getenv(sessionEnvVar); env != "" {
		if err := session.ValidateName(env); err != nil {
			return "", false, fmt.Errorf("mount: %s: %w", sessionEnvVar, err)
		}
		return env, false, nil
	}
	return "", true, nil
}

// newImplicitSession creates the single-action session a mount
// without context records into. Implicit sessions are isomorphic
// with explicit ones and answer to the same session commands.
func newImplicitSession(engine *session.Engine) (string, error) {
	name, err := session.ImplicitName(time.Now().UTC())
	if err != nil {
		return "", fmt.Errorf("mount: %w", err)
	}
	if err := engine.New(name); err != nil {
		return "", err
	}
	return name, nil
}

// mountAction builds the record action for one mount. The identity
// fields come from the resolved library target; a file-path image
// records the reference as given and leaves the identity empty.
func mountAction(ref string, target image.Target, dir string, flags mountFlags) (session.Action, error) {
	mode := session.ModeRW
	if flags.readOnly {
		mode = session.ModeRO
	}
	action := session.Action{
		Type:       session.TypeMount,
		Image:      ref,
		Mountpoint: dir,
		Mode:       mode,
		Subdir:     flags.subdir,
	}
	if target.Library {
		owner, repo, err := image.ParseName(target.Name)
		if err != nil {
			return session.Action{}, fmt.Errorf("mount: %w", err)
		}
		action.Owner, action.Repo, action.FullID = owner, repo, target.ID
	}
	return action, nil
}

// runMountEngine assembles the engine request and runs the mount,
// the execution half of one write-ahead record. Both the mount
// command and the session replay run through it.
func runMountEngine(
	cfg config.Config,
	target image.Target,
	dir string,
	flags mountFlags,
) (session.Result, error) {
	req, err := mountRequest(cfg, target, dir, flags)
	if err != nil {
		return session.Result{}, err
	}
	ws, err := mountEngine(req)
	if err != nil {
		if errors.Is(err, workspace.ErrUnavailable) {
			return session.Result{}, fmt.Errorf(
				"%w; check the environment with tiler doctor", err)
		}
		return session.Result{}, err
	}
	return session.Result{
		Mountpoint: ws.MergedDir,
		Upper:      ws.UpperDir,
		Reused:     ws.Reused,
	}, nil
}

// isResolveUsage reports whether a resolve failure rejects the
// argument shape itself, which is a command-line usage problem. A
// library state failure stays a runtime error.
func isResolveUsage(err error) bool {
	return errors.Is(err, image.ErrUnknownImage) ||
		errors.Is(err, image.ErrNotImageFile) ||
		errors.Is(err, image.ErrInvalidName) ||
		errors.Is(err, image.ErrInvalidPath)
}

// verifyMembers checks every library member against its recorded
// facts: the cheap size and mtime probe by default, the full sha256
// re-hash with --verify. A file-path target records no facts and
// skips verification.
func verifyMembers(target image.Target, verify bool) error {
	if !target.Library {
		return nil
	}
	for _, m := range target.Members {
		meta := image.Meta{Size: m.Size, MtimeUnix: m.MtimeUnix, SHA256: m.SHA256}
		var err error
		if verify {
			err = image.VerifySHA256(meta, m.Path)
		} else {
			err = image.QuickCheck(meta, m.Path)
		}
		if err != nil {
			return fmt.Errorf("mount: %w", err)
		}
	}
	return nil
}

// mountRequest assembles the engine request. With the cache enabled
// every member mounts from its verified local copy while fsname
// keeps the original path, so the mount table identity survives the
// copy.
func mountRequest(
	cfg config.Config,
	target image.Target,
	dir string,
	flags mountFlags,
) (workspace.Request, error) {
	mounted := make([]string, 0, len(target.Members))
	original := make([]string, 0, len(target.Members))
	for _, m := range target.Members {
		path := m.Path
		if cfg.CacheEnabled {
			cached, _, err := ensureCache(cfg.CacheDir, m.Path, m.SHA256)
			if err != nil {
				return workspace.Request{}, fmt.Errorf("mount: %w", err)
			}
			path = cached
		}
		mounted = append(mounted, path)
		original = append(original, m.Path)
	}
	table, err := loadMountTable()
	if err != nil {
		return workspace.Request{}, fmt.Errorf("mount: %w", err)
	}
	req := workspace.Request{
		Full:            mounted[0],
		Layers:          mounted[1:],
		ReadOnly:        flags.readOnly,
		Subdir:          flags.subdir,
		Table:           toEntries(table),
		SquashfsOptions: cfg.SquashfsOptions,
		FuseOptions:     cfg.FuseOptions,
		RuntimeDir:      cfg.RuntimeDir,
		MergedDir:       dir,
		Run:             mountRun,
	}
	if cfg.CacheEnabled {
		req.FullFSName = original[0]
		req.LayerFSNames = original[1:]
	}
	return req, nil
}

// toEntries converts mount table rows into the minimal entries the
// engine consumes, keeping the engine free of a mountinfo import.
func toEntries(table []mountinfo.Mount) []workspace.Entry {
	entries := make([]workspace.Entry, 0, len(table))
	for _, m := range table {
		entries = append(entries, workspace.Entry{
			Point:  m.Point,
			FSType: m.FSType,
			Source: m.Source,
			Upper:  m.Upper,
			Lowers: m.Lowers,
		})
	}
	return entries
}
