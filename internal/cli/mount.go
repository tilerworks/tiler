package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

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
	// assembleBlocks materializes a blocks payload into the cache.
	assembleBlocks = image.AssembleBlocks
	// mountEngine builds the workspace mount.
	mountEngine = workspace.Mount
	// forestEngine builds the members-form forest mount.
	forestEngine = workspace.MountForest
	// unmountEngine tears a view down by path.
	unmountEngine = workspace.UnmountPath
)

// mountFlags carries the local flags of one mount invocation.
type mountFlags struct {
	readOnly bool
	subdir   string
	verify   bool
	session  string
	rm       bool
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
			"Every mount is recorded write-ahead in a session: -s, --session\n" +
			"names one, the " + sessionEnvVar + " environment variable\n" +
			"binds one, and without either a generated adjective-animal\n" +
			"session is created. 'session open' later replays the record.\n" +
			"\n" +
			"--rm marks the just-created session for removal: once its last\n" +
			"view is unmounted the record is deleted and its runtime\n" +
			"purged, upper data included. --rm cannot target a session\n" +
			"named by --session or " + sessionEnvVar + "; use 'session new\n" +
			"--rm [name]' for a removable session created ahead of time.",
		Args: exactTwoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMount(cmd, args, flags)
		},
	}
	cmd.Flags().BoolVar(&flags.readOnly, "read-only", false, "mount without a writable upper")
	cmd.Flags().StringVar(&flags.subdir, "subdir", "", "image subdirectory exposed as the mount root")
	cmd.Flags().BoolVar(&flags.verify, "verify", false, "re-verify every recorded digest with sha256")
	cmd.Flags().StringVarP(&flags.session, "session", "s", "", "session that records this mount")
	cmd.Flags().BoolVar(&flags.rm, "rm", false,
		"remove the just-created session and purge its runtime when its last view unmounts")
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
	if err := verifyTarget(target, flags.verify); err != nil {
		return err
	}
	// The members form is a read-only subpath forest: a writable
	// view or a subdir root has no meaning over side-by-side
	// partitions, so both are usage problems with guidance.
	if target.Form == image.PayloadMembers {
		if !flags.readOnly {
			return usageError{errors.New(
				"mount: the members split form is a read-only partition forest; pass --read-only")}
		}
		if flags.subdir != "" {
			return usageError{errors.New(
				"mount: the members split form takes no --subdir; every partition mounts at its own subpath")}
		}
	}
	name, implicit, err := resolveMountSession(flags.session)
	if err != nil {
		return err
	}
	if flags.rm && !implicit {
		return usageError{errors.New(
			"mount: --rm only applies when this mount creates a new" +
				" session (no --session or " + sessionEnvVar + ");" +
				" it cannot target a named session;" +
				" create one with 'session new --rm [name]'")}
	}
	// A killed FUSE daemon can leave the target unlisted while its
	// session-scoped squashfuse lowers remain. Tear that residue down
	// before a fresh write-ahead mount so the new combination starts
	// clean; a healthy live view is left alone for reuse.
	preemptStaleView(cfg, dir)
	engine := sessionEngine(cfg)
	if implicit {
		if name, err = allocateGeneratedSession(engine, flags.rm, "mount"); err != nil {
			return err
		}
	}
	action, err := mountAction(args[0], target, dir, flags)
	if err != nil {
		if implicit {
			discardGeneratedSession(engine, name, false)
		}
		return err
	}
	// The write-ahead record is durable before the engine runs and
	// carries the outcome afterwards, so a crash between the two
	// leaves a replayable pending action. The stored action carries
	// RuntimeID (m<seq>), which names the combination directory.
	result, err := engine.RunMount(name, action,
		func(stored session.Action) (session.Result, error) {
			return runMountEngine(cfg, target, dir, flags,
				sessionRuntimeDir(cfg, name, stored))
		})
	if implicit && (err != nil || result.Reused) {
		// A just-created record whose action failed or merely adopted
		// an already-healthy view never serves a live view of its own;
		// keeping it would pin the image and leak an active name.
		discardGeneratedSession(engine, name, err == nil && result.Reused)
	}
	if err != nil {
		return err
	}
	// Announce a kept generated session only after the mount
	// succeeds; discarded draws must not print a reusable name.
	if implicit && !result.Reused {
		if _, err = fmt.Fprintf(cmd.OutOrStdout(),
			"new session created: %s\n", name); err != nil {
			return err
		}
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

// nameAllocAttempts is how many GenerateName draws a caller may take
// before giving up on a free session name.
const nameAllocAttempts = 16

// nameDigitAfter is the attempt index at which WithCollisionDigit
// starts appending a trailing 0-9 to diversify after collisions.
const nameDigitAfter = 8

// allocateGeneratedSession creates a session with a GenerateName
// draw, retrying on collisions. Generated sessions are isomorphic
// with explicitly named ones. With removeOnClose the record is
// marked --rm, so it deletes itself and purges its runtime once it
// closes (or its last view unmounts).
func allocateGeneratedSession(engine *session.Engine, removeOnClose bool, label string) (string, error) {
	for attempt := 0; attempt < nameAllocAttempts; attempt++ {
		name, err := session.GenerateName()
		if err != nil {
			return "", fmt.Errorf("%s: %w", label, err)
		}
		if attempt >= nameDigitAfter {
			name, err = session.WithCollisionDigit(name)
			if err != nil {
				return "", fmt.Errorf("%s: %w", label, err)
			}
		}
		if removeOnClose {
			err = engine.NewRemovable(name)
		} else {
			err = engine.New(name)
		}
		if err == nil {
			return name, nil
		}
		if !errors.Is(err, session.ErrExists) {
			return "", err
		}
	}
	return "", fmt.Errorf("%s: could not allocate a unique session name", label)
}

// discardGeneratedSession drops one just-created record that never
// came to serve a live view. A discard failure is swallowed: the
// mount outcome owns the command's error, and garbage collection
// remains the fallback for the record.
func discardGeneratedSession(engine *session.Engine, name string, reused bool) {
	_, _ = engine.Discard(name, reused)
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

// preemptStaleView tears down a dead or vanished view at dir before
// a fresh mount. A healthy live view is left alone so reuse still
// works; a listed-but-dead overlay and a vanished overlay that left
// session-scoped lowers behind both take the full unmount path.
func preemptStaleView(cfg config.Config, dir string) {
	table, err := loadMountTable()
	if err != nil || len(table) == 0 {
		return
	}
	entries := toEntries(table)
	listed := false
	for _, entry := range entries {
		if entry.Point == dir {
			listed = true
			break
		}
	}
	if listed && viewServing(dir) {
		return
	}
	runtime := resolveCombinationRuntime(cfg, dir, entries)
	if runtime == "" {
		return
	}
	_, _ = unmountEngine(workspace.UnmountRequest{
		Dir:        dir,
		Table:      entries,
		RuntimeDir: runtime,
		Owned:      ownedLibrarySource(cfg.ImagesDir),
		Reload:     reloadEntries,
		Run:        mountRun,
	})
	engine := sessionEngine(cfg)
	// Clear StatusMounted on the torn-down view without closing a
	// non--rm session, so a remount that mints a new name does not
	// compete with a dead claim when resolving the runtime path.
	_ = engine.ReleaseMount(dir)
	_, _ = engine.CloseRemovableFor(dir, sessionUmount(cfg))
}

// viewServing reports whether dir still answers reads. A dead FUSE
// connection refuses with ENOTCONN; a healthy mount or plain
// directory opens successfully.
func viewServing(dir string) bool {
	file, err := os.Open(dir)
	if err == nil {
		_ = file.Close()
		return true
	}
	return false
}

// sessionRuntimeDir is the combination directory of one recorded
// mount action: sessions/<name>/runtime/m<seq>.
func sessionRuntimeDir(cfg config.Config, name string, action session.Action) string {
	id := action.RuntimeID
	if id == "" && action.Seq > 0 {
		id = "m" + strconv.Itoa(action.Seq)
	}
	if name == "" || id == "" {
		return ""
	}
	return filepath.Join(cfg.SessionsDir, name, "runtime", id)
}

// runMountEngine assembles the engine request and runs the mount,
// the execution half of one write-ahead record. Both the mount
// command and the session replay run through it. The target's split
// form selects the engine: the single-file cascade keeps the plain
// stack mount, a blocks payload reassembles into the cache, and a
// members payload mounts the subpath forest. runtimeDir is the
// session-scoped combination directory for this action.
func runMountEngine(
	cfg config.Config,
	target image.Target,
	dir string,
	flags mountFlags,
	runtimeDir string,
) (session.Result, error) {
	switch target.Form {
	case image.PayloadBlocks:
		return runBlocksEngine(cfg, target, dir, flags, runtimeDir)
	case image.PayloadMembers:
		return runForestEngine(cfg, target, dir)
	default:
		return runStackEngine(cfg, target, dir, flags, runtimeDir)
	}
}

// runBlocksEngine mounts a blocks payload by reassembling every
// stack layer into the cache directory, content-addressed by its
// digest, then overlaying them newest-incr-first. Sessions share
// one verified copy per layer. It publishes the canonical library
// path as the mount table identity and does not stack the
// cache_enabled copy on top — the payload is already local.
func runBlocksEngine(
	cfg config.Config,
	target image.Target,
	dir string,
	flags mountFlags,
	runtimeDir string,
) (session.Result, error) {
	req, err := blocksRequest(cfg, target, dir, flags, runtimeDir)
	if err != nil {
		return session.Result{}, err
	}
	return runEngine(req)
}

// blocksRequest reassembles every blocks-form stack layer and
// builds the overlay request. Layers arrive oldest-first (full,
// then chain); the workspace reverses them so the newest wins.
func blocksRequest(
	cfg config.Config,
	target image.Target,
	dir string,
	flags mountFlags,
	runtimeDir string,
) (workspace.Request, error) {
	repo, err := image.OpenRepo(cfg.ImagesDir, target.Name)
	if err != nil {
		return workspace.Request{}, fmt.Errorf("mount: %w", err)
	}
	table, err := loadMountTable()
	if err != nil {
		return workspace.Request{}, fmt.Errorf("mount: %w", err)
	}
	layers := target.Members
	if len(layers) == 0 {
		layers = []image.Member{{
			Size: target.Size, SHA256: target.SHA256, Blocks: target.Blocks,
		}}
	}
	mounted := make([]string, 0, len(layers))
	for _, layer := range layers {
		materialized, err := assembleBlocks(
			cfg.CacheDir, layer.Blocks, layer.SHA256, layer.Size)
		if err != nil {
			return workspace.Request{}, fmt.Errorf("mount: %w", err)
		}
		mounted = append(mounted, materialized)
	}
	// The canonical library path is the mount-table identity; the
	// mounted bytes are the materialized cache copies, so the
	// fsname override keeps the table pointing at the logical image.
	canonical := repo.ImagePath()
	return workspace.Request{
		Full:            mounted[0],
		FullFSName:      canonical,
		Layers:          mounted[1:],
		ReadOnly:        flags.readOnly,
		Subdir:          flags.subdir,
		Table:           toEntries(table),
		SquashfsOptions: cfg.SquashfsOptions,
		FuseOptions:     cfg.FuseOptions,
		RuntimeDir:      runtimeDir,
		MergedDir:       dir,
		Run:             mountRun,
	}, nil
}

// runForestEngine mounts a members payload as the subpath forest
// combination: every partition read-only at its own subpath under
// the target directory, owned child mounts recognized through the
// library identity for reuse and teardown.
func runForestEngine(cfg config.Config, target image.Target, dir string) (session.Result, error) {
	table, err := loadMountTable()
	if err != nil {
		return session.Result{}, fmt.Errorf("mount: %w", err)
	}
	partitions := make([]workspace.ForestMember, 0, len(target.Partitions))
	for _, p := range target.Partitions {
		partitions = append(partitions, workspace.ForestMember{Name: p.Name, Path: p.Path})
	}
	ws, err := forestEngine(workspace.ForestRequest{
		Partitions:      partitions,
		ParentDir:       dir,
		SquashfsOptions: cfg.SquashfsOptions,
		Table:           toEntries(table),
		Owned:           ownedLibrarySource(cfg.ImagesDir),
		Run:             mountRun,
	})
	if err != nil {
		return session.Result{}, engineUnavailable(err)
	}
	return session.Result{Mountpoint: ws.MergedDir, Reused: ws.Reused}, nil
}

// runStackEngine runs the plain stack mount over an assembled
// request.
func runStackEngine(
	cfg config.Config,
	target image.Target,
	dir string,
	flags mountFlags,
	runtimeDir string,
) (session.Result, error) {
	req, err := mountRequest(cfg, target, dir, flags, runtimeDir)
	if err != nil {
		return session.Result{}, err
	}
	return runEngine(req)
}

// runEngine executes one assembled stack request through the engine
// seam and reduces the workspace to the recorded result.
func runEngine(req workspace.Request) (session.Result, error) {
	ws, err := mountEngine(req)
	if err != nil {
		return session.Result{}, engineUnavailable(err)
	}
	return session.Result{
		Mountpoint: ws.MergedDir,
		Upper:      ws.UpperDir,
		Reused:     ws.Reused,
	}, nil
}

// engineUnavailable points an unavailable mount environment at the
// doctor command.
func engineUnavailable(err error) error {
	if errors.Is(err, workspace.ErrUnavailable) {
		return fmt.Errorf("%w; check the environment with tiler doctor", err)
	}
	return err
}

// ownedLibrarySource builds the ownership predicate the forest
// mount and the path unmount share: a mount source is owned when it
// resolves to an identity inside the image library. The closure keeps
// the workspace engine free of a mountinfo dependency.
func ownedLibrarySource(imagesDir string) func(string) bool {
	return func(source string) bool {
		_, ok := mountinfo.LibraryName(imagesDir, source)
		return ok
	}
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

// verifyTarget runs the pre-mount integrity check of the resolved
// target: the cheap size and mtime probe by default, the full sha256
// re-hash with --verify. A split payload checks every manifest block
// or partition entry; the single-file cascade checks its stack
// members. A file-path target records no facts and skips
// verification.
func verifyTarget(target image.Target, verify bool) error {
	if !target.Library {
		return nil
	}
	switch target.Form {
	case image.PayloadBlocks, image.PayloadMembers:
		if verify {
			return image.VerifyTarget(target)
		}
		return image.QuickCheckTarget(target)
	default:
		return verifyMembers(target, verify)
	}
}

// verifyMembers checks every stack member of a single-file cascade
// against its recorded facts: the cheap size and mtime probe by
// default, the full sha256 re-hash with --verify.
func verifyMembers(target image.Target, verify bool) error {
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
	runtimeDir string,
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
		RuntimeDir:      runtimeDir,
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
