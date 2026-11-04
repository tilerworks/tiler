package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jiangxin/tiler/internal/config"
	"github.com/jiangxin/tiler/internal/container"
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
	// mountNotice receives short diagnostics that do not fail the
	// mount, such as a skipped unknown digest.
	mountNotice io.Writer = os.Stderr
	// assembleBlocks materializes a blocks payload into the cache.
	assembleBlocks = image.AssembleBlocks
	// mountEngine builds the workspace mount.
	mountEngine = workspace.Mount
	// unmountEngine tears a view down by path.
	unmountEngine = workspace.UnmountPath
)

// mountTableOrEmpty reads the host mount table. A host that cannot
// read one has no rows that would show an existing view, so replay
// and mount see an empty snapshot instead of failing before attach.
// Status uses the same rule for an empty store. Detach does not:
// a missing table falls through to hdiutil info.
func mountTableOrEmpty() ([]mountinfo.Mount, error) {
	table, err := loadMountTable()
	if errors.Is(err, mountinfo.ErrUnavailable) {
		return nil, nil
	}
	return table, err
}

// mountFlags carries the local flags of one mount invocation.
type mountFlags struct {
	readOnly bool
	subdir   string
	verify   bool
	session  string
	rm       bool
	// pruneUpper asks the engine to prune a reused upper against
	// the lower stack before merging. Only session replay sets it,
	// when the recorded served stack resolves differently from the
	// desired stack; the mount command leaves it false.
	pruneUpper bool
	// rebaseInstance binds a replay journal to the session incarnation.
	rebaseInstance string
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
			"default, or a read-only view with --read-only. <image> is the\n" +
			"path of one squashfs or erofs file, or a 1-to-5 segment library\n" +
			"name. When the cleaned path exists as a regular file, that\n" +
			"file wins over a library name. Import an external image with\n" +
			"build --link or build --copy, then mount the library name.\n" +
			"Chain layers mount as a cascade, --subdir exposes one image\n" +
			"subdirectory as the mount root, and --verify re-hashes every\n" +
			"recorded digest instead of the default quick check. A digest\n" +
			"of 64 zeros is unknown: --verify skips that sha256, still\n" +
			"checks reachability, format, size, and mtime, and\n" +
			"reports the skip. <dir> must be an empty directory owned\n" +
			"by the current user, outside every reserved tree, and must\n" +
			"not cover the sessions_dir, images_dir, or cache_dir paths\n" +
			"from .tiler.yaml, or $HOME/.tiler.\n" +
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
	// A missing FUSE stack blocks every format except dmg, which
	// attaches with hdiutil. A non-dmg host still refuses before
	// resolve, so a missing image reports the toolchain gap.
	stackErr := gateAnyStack("mount")
	if stackErr != nil && !darwinHost() {
		return stackErr
	}
	dir, err := filepath.Abs(args[1])
	if err != nil {
		return fmt.Errorf("mount: target: %w", err)
	}
	if err := config.OutsideReserved(dir, cfg); err != nil {
		return usageError{err}
	}
	// A target that sits above sessions_dir (or another reserved
	// root) would hide that tree under the view and put upper/work
	// storage inside the merged path — refuse before write-ahead.
	if err := config.CoversReserved(dir, cfg); err != nil {
		return usageError{err}
	}
	if err := workspace.CheckSubdir(flags.subdir); err != nil {
		return usageError{err}
	}
	selector := args[0]
	target, err := resolveRuntimeMount(cfg, selector)
	if err != nil {
		if isResolveUsage(err) {
			return usageError{err}
		}
		return err
	}
	engine := sessionEngine(cfg)
	coordinated := target.Library && engine.Versioned
	var blockPaths []string
	if !coordinated {
		blockPaths, err = validateMountTarget(cfg, target, flags, stackErr)
		if err != nil {
			return err
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
	if implicit {
		if name, err = allocateGeneratedSession(engine, flags.rm, "mount"); err != nil {
			return err
		}
	}
	var result session.Result
	if coordinated {
		err = resolveValidatedVersionAndPin(cfg, name, selector, flags, stackErr,
			func(local *session.Engine, pinned image.Target) error {
				pinnedAction, actionErr := mountAction(args[0], pinned, dir, flags)
				if actionErr != nil {
					return actionErr
				}
				result, actionErr = local.RunMount(name, pinnedAction,
					func(stored session.Action) (session.Result, error) {
						return runVersionMountEngine(cfg, name, stored, dir, flags,
							sessionRuntimeDir(cfg, name, stored))
					})
				return actionErr
			})
	} else {
		action, actionErr := mountAction(args[0], target, dir, flags)
		if actionErr != nil {
			if implicit {
				discardGeneratedSession(engine, name, false)
			}
			return actionErr
		}
		result, err = engine.RunMount(name, action,
			func(stored session.Action) (session.Result, error) {
				return runMountEngine(cfg, target, dir, flags,
					sessionRuntimeDir(cfg, name, stored), blockPaths)
			})
	}
	if implicit && (err != nil || result.Reused) {
		discardGeneratedSession(engine, name, err == nil && result.Reused)
	}
	if err != nil {
		return err
	}
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

func resolveValidatedVersionAndPin(cfg config.Config, sessionName, selector string,
	flags mountFlags, stackErr error,
	callback func(*session.Engine, image.Target) error,
) error {
	return resolveVersionAndPin(cfg, sessionName, selector,
		func(local *session.Engine, pinned image.Target) error {
			if _, err := validateMountTarget(cfg, pinned, flags, stackErr); err != nil {
				return err
			}
			return callback(local, pinned)
		})
}

func validateMountTarget(cfg config.Config, target image.Target, flags mountFlags, stackErr error) ([]string, error) {
	if flags.verify && !target.Library {
		return nil, usageError{errors.New(
			"mount: --verify needs a library image; a file path records no digest")}
	}
	if err := verifyTarget(target, flags.verify); err != nil {
		return nil, err
	}
	// A file path records no format. Detect it before --subdir is
	// judged, so a koly file is a dmg usage error. A library image
	// already names its type, and formatForGate returns that without
	// reading the file. The magic guard stays after this check: a
	// dmg --subdir is a usage error even when the bytes would also
	// fail the magic match.
	format, err := formatForGate(target, nil)
	if err != nil {
		return nil, err
	}
	if err := refuseDMGSubdir(flags.subdir, format); err != nil {
		return nil, err
	}
	// Unknown magic and a meta/magic mismatch cannot succeed on
	// replay. Refuse them before the write-ahead action is appended,
	// or session open retries the bad mount and stops restoring
	// later views in the same record.
	blockPaths, err := guardMountFormat(cfg, target)
	if err != nil {
		return nil, err
	}
	format, err = formatForGate(target, blockPaths)
	if err != nil {
		return nil, err
	}
	if stackErr != nil && format != image.FormatDMG {
		return nil, stackErr
	}
	if err := gateFormatTools("mount", format); err != nil {
		return nil, err
	}
	return blockPaths, nil
}

func resolveRuntimeMount(cfg config.Config, selector string) (image.Target, error) {
	var target image.Target
	err := image.WithPreparedTarget(cfg.ImagesDir, func() error {
		var resolveErr error
		target, resolveErr = image.ResolveVersionMount(cfg, selector)
		return resolveErr
	})
	return target, err
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
// records its complete content digest as the stable tip identity.
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
		segs, err := image.ParseName(target.Name)
		if err != nil {
			return session.Action{}, fmt.Errorf("mount: %w", err)
		}
		action.Name, action.FullID = strings.Join(segs, "/"), target.ID
		// The resolved stack tip pins the desired stack, so a later
		// replay remounts the same layers even after the library
		// head moves past them.
		action.TipID = target.Tip
	} else {
		file, err := os.Open(ref)
		if err != nil {
			return session.Action{}, fmt.Errorf("mount: hash image: %w", err)
		}
		digest := sha256.New()
		if _, err := io.Copy(digest, file); err != nil {
			_ = file.Close()
			return session.Action{}, fmt.Errorf("mount: hash image: %w", err)
		}
		if err := file.Close(); err != nil {
			return session.Action{}, fmt.Errorf("mount: hash image: %w", err)
		}
		action.Image, err = filepath.Abs(ref)
		if err != nil {
			return session.Action{}, fmt.Errorf("mount: image path: %w", err)
		}
		action.TipID = hex.EncodeToString(digest.Sum(nil))
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
// stack mount, and a blocks payload reassembles into the cache.
// runtimeDir is the session-scoped combination directory for this
// action. blockPaths, when non-nil, are blocks layers the mount
// command already reassembled so a magic refusal could return
// before this record existed. Replay passes nil and assembles here.
func runMountEngine(
	cfg config.Config,
	target image.Target,
	dir string,
	flags mountFlags,
	runtimeDir string,
	blockPaths []string,
) (session.Result, error) {
	format, err := formatForGate(target, blockPaths)
	if err != nil {
		return session.Result{}, err
	}
	if format != image.FormatDMG {
		if err := gateAnyStack("mount"); err != nil {
			return session.Result{}, err
		}
	}
	if err := gateFormatTools("mount", format); err != nil {
		return session.Result{}, err
	}
	if target.Form == image.PayloadBlocks {
		return runBlocksEngine(cfg, target, dir, flags, runtimeDir, blockPaths)
	}
	return runStackEngine(cfg, target, dir, flags, runtimeDir)
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
	blockPaths []string,
) (session.Result, error) {
	req, err := blocksRequest(cfg, target, dir, flags, runtimeDir, blockPaths)
	if err != nil {
		return session.Result{}, err
	}
	return runEngine(req)
}

// blocksRequest builds the overlay request for a blocks payload.
// blockPaths are layers the mount command already reassembled so a
// magic refusal could return before write-ahead; nil means this
// call reassembles them, which is the session replay path. Layers
// arrive oldest-first (full, then chain); the workspace reverses
// them so the newest wins.
func blocksRequest(
	cfg config.Config,
	target image.Target,
	dir string,
	flags mountFlags,
	runtimeDir string,
	blockPaths []string,
) (workspace.Request, error) {
	repo, err := image.OpenRepo(cfg.ImagesDir, target.Name)
	if err != nil {
		return workspace.Request{}, fmt.Errorf("mount: %w", err)
	}
	table, err := mountTableOrEmpty()
	if err != nil {
		return workspace.Request{}, fmt.Errorf("mount: %w", err)
	}
	mounted := blockPaths
	if mounted == nil {
		mounted, err = materializeBlockLayers(cfg, target)
		if err != nil {
			return workspace.Request{}, err
		}
	}
	format, err := resolveRequestFormat(target, mounted)
	if err != nil {
		return workspace.Request{}, err
	}
	// The canonical library path is the mount-table identity; the
	// mounted bytes are the materialized cache copies, so the
	// fsname override keeps the table pointing at the logical image.
	canonical := repo.ImagePath()
	return workspace.Request{
		MaxLayers:       cfg.LayerLimit(),
		Full:            mounted[0],
		FullFSName:      canonical,
		Layers:          mounted[1:],
		Format:          format,
		ReadOnly:        flags.readOnly,
		PruneUpper:      flags.pruneUpper,
		Subdir:          flags.subdir,
		Table:           toEntries(table),
		SquashfsOptions: cfg.SquashfsOptions,
		FuseOptions:     cfg.FuseOptions,
		RuntimeDir:      runtimeDir,
		MergedDir:       dir,
		Run:             mountRun,
		Progress:        mountNotice,
	}, nil
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
		Mountpoint:      ws.MergedDir,
		Upper:           ws.UpperDir,
		Reused:          ws.Reused,
		PruneIncomplete: incompleteUpperPrune(req, ws),
	}, nil
}

// incompleteUpperPrune reports a requested prune of a non-empty
// upper whose window did not succeed. PruneFailed is set only when
// that window was entered, so an empty upper, a request that did
// not ask to prune, and a window that finished stay false and the
// replay clears the served pins.
func incompleteUpperPrune(req workspace.Request, ws *workspace.Workspace) bool {
	// Reconciliation has already made this upper valid for the desired stack.
	// Redundant-file pruning can warn without keeping obsolete served pins.
	return req.Rebase == nil && req.PruneUpper && ws.PruneFailed
}

// engineUnavailable points an unavailable mount environment at the
// doctor command.
func engineUnavailable(err error) error {
	if errors.Is(err, workspace.ErrUnavailable) {
		return fmt.Errorf("%w; check the environment with tiler doctor", err)
	}
	return err
}

// ownedLibrarySource builds the ownership predicate path unmount
// shares: a mount source is owned when it resolves to an identity
// inside the image library. The closure keeps the workspace engine
// free of a mountinfo dependency.
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
// re-hash with --verify. A blocks payload checks every manifest
// block; the single-file cascade checks its stack members. An
// unknown single-file digest keeps the quick probe under --verify.
// A file-path target records no facts and skips verification.
func verifyTarget(target image.Target, verify bool) error {
	if !target.Library {
		return nil
	}
	if target.Form == image.PayloadBlocks {
		if verify {
			return image.VerifyTarget(target)
		}
		return image.QuickCheckTarget(target)
	}
	return verifyMembers(target, verify)
}

// verifyMembers checks every stack member of a single-file cascade
// against its recorded facts: the cheap size and mtime probe by
// default, the full sha256 re-hash with --verify. An unknown member
// digest keeps the quick probe even under --verify. Known members
// and every block digest stay on the requested probe.
func verifyMembers(target image.Target, verify bool) error {
	for _, m := range target.Members {
		if err := verifyMember(m, verify); err != nil {
			return fmt.Errorf("mount: %w", err)
		}
	}
	return nil
}

func verifyMember(m image.Member, verify bool) error {
	if len(m.Blocks) != 0 {
		return verifyMemberBlocks(m.Blocks, verify)
	}
	node := image.ImageMeta{Size: m.Size, MtimeUnix: m.MtimeUnix, SHA256: m.SHA256}
	skipped, err := image.VerifyMountDigest(node, m.Path, verify)
	if err != nil {
		return err
	}
	if skipped {
		noteUnknownDigest(m.ID)
	}
	return nil
}

func verifyMemberBlocks(blocks []image.BlockFile, verify bool) error {
	for _, block := range blocks {
		node := image.ImageMeta{Size: block.Size, MtimeUnix: block.MtimeUnix,
			SHA256: block.SHA256}
		var err error
		if verify {
			err = image.VerifySHA256(node, block.Path)
		} else {
			err = image.QuickCheck(node, block.Path)
		}
		if err != nil {
			return fmt.Errorf("block %s: %w", block.Name, err)
		}
	}
	return nil
}

func noteUnknownDigest(id string) {
	if id == "" {
		id = "image"
	}
	_, _ = writeWarning(mountNotice, "mount", "%s: digest unknown, skipped sha256 verification\n", listID(id))
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
	original := memberPaths(target)
	bytesPaths, err := payloadBytesPaths(original)
	if err != nil {
		return workspace.Request{}, err
	}
	format, err := resolveRequestFormat(target, bytesPaths)
	if err != nil {
		return workspace.Request{}, err
	}
	mounted := make([]string, 0, len(target.Members))
	keepIdentity := cfg.CacheEnabled
	for i, m := range target.Members {
		path := bytesPaths[i]
		if path != original[i] {
			keepIdentity = true
		}
		if cfg.CacheEnabled {
			cached, _, err := ensureCache(cfg.CacheDir, m.Path, m.SHA256)
			if err != nil {
				return workspace.Request{}, fmt.Errorf("mount: %w", err)
			}
			path = cached
		}
		mounted = append(mounted, path)
	}
	table, err := mountTableOrEmpty()
	if err != nil {
		return workspace.Request{}, fmt.Errorf("mount: %w", err)
	}
	req := workspace.Request{
		MaxLayers:       cfg.LayerLimit(),
		Full:            mounted[0],
		Layers:          mounted[1:],
		Format:          format,
		ReadOnly:        flags.readOnly,
		PruneUpper:      flags.pruneUpper,
		Subdir:          flags.subdir,
		Table:           toEntries(table),
		SquashfsOptions: cfg.SquashfsOptions,
		FuseOptions:     cfg.FuseOptions,
		RuntimeDir:      runtimeDir,
		MergedDir:       dir,
		Run:             mountRun,
		Progress:        mountNotice,
	}
	if keepIdentity {
		req.FullFSName = original[0]
		req.LayerFSNames = original[1:]
	}
	return req, nil
}

// payloadBytesPaths resolves each library leaf (symlink or path-ref)
// to the container file FUSE or hdiutil should open.
func payloadBytesPaths(paths []string) ([]string, error) {
	out := make([]string, len(paths))
	for i, path := range paths {
		if path == "" {
			out[i] = path
			continue
		}
		bytesPath, err := image.ResolvePayloadBytesPath(path)
		if err != nil {
			return nil, fmt.Errorf("mount: %w", err)
		}
		out[i] = bytesPath
	}
	return out, nil
}

// guardMountFormat refuses unknown or mismatched container magic
// before a mount action is appended. File-path and single-file
// library paths are already known. Versioned blocks are verified directly
// when concatfs is available, or reassembled into the cache otherwise.
// Legacy blocks return materialized paths for the action callback.
func guardMountFormat(cfg config.Config, target image.Target) ([]string, error) {
	versioned := target.Library && len(target.Members) != 0
	for _, member := range target.Members {
		versioned = versioned && member.Format != ""
	}
	if versioned {
		for _, member := range target.Members {
			format := member.Format
			if format == "" {
				format = target.Format
			}
			if len(member.Blocks) != 0 {
				// Both routes check every known block digest before
				// write-ahead. Format magic is read from the resolved
				// block windows, not from position-file text.
				if concatAvailable() {
					if err := image.VerifyBlockPayload(member.Blocks, member.SHA256, member.Size); err != nil {
						return nil, err
					}
				} else if _, err := materializeBlockLayers(cfg, image.Target{
					Library: true, Format: format, Form: image.PayloadBlocks,
					Members: []image.Member{member},
				}); err != nil {
					return nil, err
				}
				if err := matchResolvedBlockFormat(member, format); err != nil {
					return nil, err
				}
				continue
			}
			one := image.Target{Library: true, Format: format,
				Members: []image.Member{member}}
			paths, err := payloadBytesPaths([]string{member.Path})
			if err != nil {
				return nil, err
			}
			if _, err := matchLibraryFormat(one, paths); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}
	if target.Form == image.PayloadBlocks {
		mounted, err := materializeBlockLayers(cfg, target)
		if err != nil {
			return nil, err
		}
		if _, err := resolveRequestFormat(target, mounted); err != nil {
			return nil, err
		}
		return mounted, nil
	}
	paths := memberPaths(target)
	bytesPaths, err := payloadBytesPaths(paths)
	if err != nil {
		return nil, err
	}
	_, err = resolveRequestFormat(target, bytesPaths)
	return nil, err
}

// memberPaths lists single-file cascade paths in stack order. A
// file-path image contributes its one file. Blocks layers keep empty
// paths until reassembly fills the cache copies.
func memberPaths(target image.Target) []string {
	paths := make([]string, len(target.Members))
	for i, member := range target.Members {
		paths[i] = member.Path
	}
	return paths
}

// materializeBlockLayers reassembles every blocks-form stack layer
// into the content-addressed cache, oldest first. An empty member
// list falls back to the full-image block manifest.
func materializeBlockLayers(cfg config.Config, target image.Target) ([]string, error) {
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
			return nil, fmt.Errorf("mount: %w", err)
		}
		mounted = append(mounted, materialized)
	}
	return mounted, nil
}

// formatForGate is the container type the toolchain gate checks.
// Library metadata already names it. A file path is detected, which
// guardMountFormat has already refused when the magic is unknown.
func formatForGate(target image.Target, blockPaths []string) (string, error) {
	if target.Format != "" {
		return target.Format, nil
	}
	paths := memberPaths(target)
	if len(blockPaths) > 0 {
		paths = blockPaths
	}
	return resolveRequestFormat(target, paths)
}

// resolveRequestFormat chooses the container type for one mount.
// A file path is detected from magic and refused when no format
// claims it. A library image must match the type recorded in
// metadata on every file that will be mounted.
func resolveRequestFormat(target image.Target, paths []string) (string, error) {
	if !target.Library {
		return detectMountFile(paths)
	}
	return matchLibraryFormat(target, paths)
}

// detectMountFile reads the magic of one file-path image. Unknown
// or ambiguous magic is a usage error, raised before a FUSE client
// is chosen for the wrong container.
func detectMountFile(paths []string) (string, error) {
	if len(paths) != 1 || paths[0] == "" {
		return "", errors.New("mount: file path has no image file")
	}
	format, err := container.DetectFile(paths[0])
	if err != nil {
		if errors.Is(err, container.ErrNotDetected) || errors.Is(err, container.ErrAmbiguous) {
			return "", usageError{fmt.Errorf("mount: %w", err)}
		}
		return "", fmt.Errorf("mount: %w", err)
	}
	return format.Type(), nil
}

// matchLibraryFormat refuses a library mount whose files do not
// carry the magic of the type recorded in metadata.
func matchLibraryFormat(target image.Target, paths []string) (string, error) {
	if target.Format == "" {
		return "", fmt.Errorf("mount: %s records no container format", target.Name)
	}
	want, err := container.Lookup(target.Format)
	if err != nil {
		return "", fmt.Errorf("mount: %w", err)
	}
	for _, path := range paths {
		if err := matchOneFormat(path, want); err != nil {
			return "", err
		}
	}
	return want.Type(), nil
}

// matchResolvedBlockFormat compares metadata with the container magic
// of the resolved block payloads. It does not read position-file text.
// A missing or unreadable block stays a block-location error. Only a
// detected type, or ErrNotDetected, is a format mismatch.
func matchResolvedBlockFormat(member image.Member, format string) error {
	got, err := image.ProbeLogicalFormat(member.Blocks)
	what := member.ID
	if what == "" && len(member.Blocks) > 0 {
		what = member.Blocks[0].Name
	}
	if errors.Is(err, container.ErrNotDetected) {
		return fmt.Errorf("mount: %s does not match meta format %s: %w", what, format, err)
	}
	if err != nil {
		return err
	}
	if got != format {
		return fmt.Errorf("mount: %s is %s but meta records %s", what, got, format)
	}
	return nil
}

// matchOneFormat reports when path is not the container want names.
func matchOneFormat(path string, want container.Format) error {
	bytesPath, err := image.ResolvePayloadBytesPath(path)
	if err != nil {
		return fmt.Errorf("mount: %s does not match meta format %s: %w",
			path, want.Type(), err)
	}
	got, err := container.DetectFile(bytesPath)
	if err != nil {
		return fmt.Errorf("mount: %s does not match meta format %s: %w",
			path, want.Type(), err)
	}
	if got.Type() != want.Type() {
		return fmt.Errorf("mount: %s is %s but meta records %s",
			path, got.Type(), want.Type())
	}
	return nil
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
