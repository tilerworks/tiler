package cli

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jiangxin/tiler/internal/activity"
	"github.com/jiangxin/tiler/internal/config"
	"github.com/jiangxin/tiler/internal/image"
	"github.com/jiangxin/tiler/internal/mountinfo"
	"github.com/jiangxin/tiler/internal/session"
	"github.com/jiangxin/tiler/internal/workspace"
	"github.com/spf13/cobra"
)

func newSessionCmd() *cobra.Command {
	cmd := newSessionListCmd()
	cmd.Use = "session"
	cmd.Aliases = []string{"sessions"}
	cmd.Short = "Manage mount sessions"
	cmd.Long = "A session is the durable record of an ordered set of mounts\n" +
		"and the unit of crash recovery. Every action is recorded\n" +
		"before it runs, so after a killed process or daemon one\n" +
		"'session open' replays the record and restores every mount.\n" +
		"'session close' tears the mounts down in reverse order and\n" +
		"keeps the record for audit; 'session remove' deletes a\n" +
		"closed record. Without a subcommand, list sessions."
	addDefaultListHint(cmd)
	cmd.AddCommand(newSessionNewCmd())
	cmd.AddCommand(newSessionOpenCmd())
	cmd.AddCommand(newSessionCloseCmd())
	cmd.AddCommand(newSessionRemoveCmd())
	cmd.AddCommand(newSessionGCCmd())
	cmd.AddCommand(newSessionListCmd())
	cmd.AddCommand(newSessionShowCmd())
	cmd.AddCommand(newStatusCmd())
	return cmd
}

func newSessionNewCmd() *cobra.Command {
	var removeOnClose bool
	cmd := &cobra.Command{
		Use:   "new [name]",
		Short: "Create an empty session",
		Long: "New creates an empty session record. Mounts join it through\n" +
			"the mount command's --session flag or the TILER_SESSION_ID\n" +
			"environment variable. Omit the name to generate an\n" +
			"adjective-animal name. A name that already exists is refused.\n" +
			"With --rm the session deletes itself when it closes: 'session\n" +
			"close' removes the record and purges the runtime of every view,\n" +
			"upper data included, instead of keeping the record for audit.",
		Args: maxOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSessionNew(cmd, args, removeOnClose)
		},
	}
	cmd.Flags().BoolVar(&removeOnClose, "rm", false,
		"delete the session and purge its runtime when it closes")
	return cmd
}

// runSessionNew creates one empty session: a caller-supplied name,
// or a GenerateName draw with the same collision retry mount uses.
func runSessionNew(cmd *cobra.Command, args []string, removeOnClose bool) error {
	cfg, ok := loadedConfig(cmd)
	if !ok {
		return errors.New("session new: configuration is not loaded")
	}
	engine := sessionEngine(cfg)
	var name string
	var err error
	if len(args) == 1 {
		name = args[0]
		if err := session.ValidateName(name); err != nil {
			return usageError{err}
		}
		if removeOnClose {
			err = engine.NewRemovable(name)
		} else {
			err = engine.New(name)
		}
	} else {
		name, err = allocateGeneratedSession(engine, removeOnClose, "session new")
	}
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "created %s\n", name)
	return err
}

func newSessionOpenCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "open <name>",
		Short: "Restore or ensure the session's mounts",
		Long: "Open replays the session record in mount order: a healthy\n" +
			"view is skipped, a dead or missing one is cleaned and\n" +
			"remounted, so the command is idempotent and is the recovery\n" +
			"entry point after a crash. A view whose image was rebuilt\n" +
			"while it stayed live remounts onto the new stack, and the\n" +
			"reused upper is pruned of what the new lower stack already\n" +
			"carries. A closed session resumes the same way: its views\n" +
			"remount and the record becomes active again.",
		Args: exactOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, engine, err := sessionCommandEngine(cmd, "session open", args[0])
			if err != nil {
				return err
			}
			mount := sessionMount(cfg, args[0], cmd.ErrOrStderr())
			if err := engine.Open(args[0], mount); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "opened %s\n", args[0])
			return err
		},
	}
}

func newSessionCloseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "close <name>",
		Short: "Unmount everything and close the record",
		Long: "Close unmounts every recorded view in reverse mount order and\n" +
			"then marks the record closed. The record is kept for audit;\n" +
			"upper data survives, and 'session remove' deletes it later. A\n" +
			"session created with --rm is deleted instead: its record is\n" +
			"removed and the runtime of every view is purged, upper data\n" +
			"included.",
		Args: exactOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, engine, err := sessionCommandEngine(cmd, "session close", args[0])
			if err != nil {
				return err
			}
			removed, err := engine.Close(args[0], sessionUmount(cfg))
			if err != nil {
				return err
			}
			verb := "closed"
			if removed {
				verb = "removed"
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", verb, args[0])
			return err
		},
	}
}

func newSessionRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "remove <name>",
		Aliases: []string{"rm"},
		Short:   "Delete a closed session record",
		Long: "Remove deletes the record of a closed session. An active\n" +
			"session is refused: close it first, so a session that still\n" +
			"holds mounts is never orphaned.",
		Args: exactOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, engine, err := sessionCommandEngine(cmd, "session remove", args[0])
			if err != nil {
				return err
			}
			if err := engine.Remove(args[0]); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "removed %s\n", args[0])
			return err
		},
	}
}

func newSessionListCmd() *cobra.Command {
	var asJSON, verbose bool
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List every session record",
		Long: "Print one aligned row per session: name, state, live\n" +
			"mount count, created, closed and last activity times, and an RM\n" +
			"marker for --rm sessions. Times follow image list formatting.\n" +
			"--verbose shows each session and its layered mounts.\n" +
			"--json renders one machine-readable\n" +
			"document instead of the human listing.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, ok := loadedConfig(cmd)
			if !ok {
				return errors.New("session list: configuration is not loaded")
			}
			engine := sessionEngine(cfg)
			if verbose && !asJSON {
				return runStatusQuery(cmd, engine, session.Query{}, false, false, true)
			}
			records, err := engine.List()
			if err != nil {
				return err
			}
			summaries := session.Summaries(records)
			for i := range summaries {
				loadSessionActivity(engine, &summaries[i], cmd.ErrOrStderr())
			}
			if asJSON {
				return writeSessionListJSON(cmd.OutOrStdout(), summaries)
			}
			return writeSessionList(cmd.OutOrStdout(), summaries)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "render the listing as a JSON document")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "show detailed layered mounts")
	return cmd
}

func newSessionShowCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "show <name>",
		Short: "Show one session record in detail",
		Long: "Show the session and its layered mounts, including live health.\n" +
			"--json preserves the record object with recent activity fields.",
		Args: exactOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, engine, err := sessionCommandEngine(cmd, "session show", args[0])
			if err != nil {
				return err
			}
			if !asJSON {
				return runStatusQuery(cmd, engine, session.Query{Session: args[0]}, false, false, true)
			}
			rec, err := engine.Show(args[0])
			if err != nil {
				return err
			}
			summary := session.Summarize(rec)
			loadSessionActivity(engine, &summary, cmd.ErrOrStderr())
			return writeJSON(cmd.OutOrStdout(), struct {
				session.Record
				sessionActivityJSON
			}{rec, newSessionActivityJSON(summary.Activity)})
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "render the record as a JSON document")
	return cmd
}

// sessionCommandEngine loads the configuration, validates the name
// argument, and assembles the lifecycle engine. A bad name shape is
// a usage problem; everything the engine reports stays a runtime
// error.
func sessionCommandEngine(cmd *cobra.Command, label, name string) (
	config.Config, *session.Engine, error,
) {
	cfg, ok := loadedConfig(cmd)
	if !ok {
		return config.Config{}, nil, fmt.Errorf("%s: configuration is not loaded", label)
	}
	if err := session.ValidateName(name); err != nil {
		return config.Config{}, nil, usageError{err}
	}
	return cfg, sessionEngine(cfg), nil
}

// sessionEngine assembles the lifecycle engine over the loaded
// configuration. The table loads through the same seam the other
// commands use; health and clock stay the real implementations. It
// is a variable so command tests can no-op the advisory lock, the
// same seam discipline the engines follow.
var sessionEngine = func(cfg config.Config) *session.Engine {
	engine := &session.Engine{
		Store:     session.Store{Dir: cfg.SessionsDir, LogsDir: cfg.LogsDir},
		Versioned: true,
		LoadTable: func() ([]mountinfo.Mount, error) {
			return loadMountTable()
		},
		SessionsDir:  cfg.SessionsDir,
		ImagesDir:    cfg.ImagesDir,
		CacheDir:     cfg.CacheDir,
		CacheEnabled: cfg.CacheEnabled,
	}
	engine.HealthyMount = func(name string, action session.Action) error {
		return recordHealthyMountActivity(cfg, name, action)
	}
	engine.ReplayReady = func(rec session.Record, action session.Action) (bool, error) {
		if action.Name == "" || action.ServedTip == "" {
			return false, nil
		}
		return workspace.RebaseReady(sessionRuntimeDir(cfg, rec.Name, action), rebaseIdentity(rec.InstanceID, action))
	}
	engine.MountRecorded = func(rec session.Record, action session.Action) error {
		if action.Name == "" || action.ServedTip != "" {
			return nil
		}
		return workspace.CompleteRebase(sessionRuntimeDir(cfg, rec.Name, action), rebaseIdentity(rec.InstanceID, action))
	}
	if readHdiutilInfo != nil {
		reader := readHdiutilInfo
		engine.LoadDiskImages = func() ([]session.DiskImage, error) {
			return diskImagesFromInfo(reader)
		}
	}
	return engine
}

// resolveStackTip is a variable so command tests can inject a resolver.
// Production resolves the exact immutable tip recorded by the session.
var resolveStackTip = func(cfg config.Config, name, fullID, tipID string) (image.Target, error) {
	target, err := image.ResolveVersionMount(cfg, name+":"+tipID)
	if err == nil && target.ID != fullID {
		return image.Target{}, fmt.Errorf("recorded root %s does not match tip %s", fullID, tipID)
	}
	return target, err
}

// sessionMount resolves one recorded action back into an engine
// request: a pin (name + FullID + TipID) is the desired stack the
// record froze at mount or rebase time. A rebased record also names
// the served stack its live view froze at; when that stack resolves
// to other lower layers than the desired stack, the request asks the
// engine to preserve deletions and prune redundant upper entries before
// switching lowers. Deletion reconciliation must succeed; redundant-file
// pruning can warn and continue. Execution shares the mount command's engine
// half and reuses sessions/<name>/runtime/m<seq>. Legacy prune warnings print
// to warn.
func sessionMount(cfg config.Config, name string, warn io.Writer) session.MountFunc {
	return func(action session.Action) (session.Result, error) {
		var (
			target image.Target
			err    error
		)
		if action.Name != "" && action.FullID != "" {
			target, err = resolveStackTip(cfg, action.Name, action.FullID, action.TipID)
		} else {
			target, err = image.ResolveVersionMount(cfg, action.Image)
		}
		if err != nil {
			return session.Result{}, fmt.Errorf("session: replay %s: %w", action.Image, err)
		}
		flags := mountFlags{readOnly: action.Mode == session.ModeRO, subdir: action.Subdir}
		if action.Name != "" && versionTarget(target) {
			if action.ServedTip != "" && !flags.readOnly && target.Format != "dmg" {
				rec, err := (session.Store{Dir: cfg.SessionsDir}).Load(name)
				if err != nil {
					return session.Result{}, err
				}
				flags.rebaseInstance = rec.InstanceID
			}
			return runVersionMountEngine(cfg, name, action, action.Mountpoint, flags,
				sessionRuntimeDir(cfg, name, action))
		}
		flags.pruneUpper = replayPruneUpper(cfg, action, target, warn)
		return runMountEngine(cfg, target, action.Mountpoint, flags,
			sessionRuntimeDir(cfg, name, action), nil)
	}
}

// recordHealthyMountActivity renews the image activity of one view a replay
// adopted because it still answers reads. The mount never runs again, so this
// record is the only evidence that the healthy open kept the stack in use. A
// file mount carries no library pin and records nothing.
func recordHealthyMountActivity(cfg config.Config, name string, action session.Action) error {
	if action.Name == "" || action.FullID == "" {
		return nil
	}
	stack, err := action.VersionStack(cfg.ImagesDir, true)
	if err != nil {
		return fmt.Errorf("session: activity %s: %w", action.Image, err)
	}
	return recordVersionStackActivity(cfg, name, action, stack, activity.PhaseSucceeded, nil)
}

func versionTarget(target image.Target) bool {
	for _, member := range target.Members {
		if strings.Contains(filepath.ToSlash(member.Path), "/_objects/") {
			return true
		}
		for _, block := range member.Blocks {
			if strings.Contains(filepath.ToSlash(block.Path), "/_objects/") {
				return true
			}
		}
	}
	return false
}

// replayPruneUpper reports whether replaying one recorded action
// must prune the reused upper before the merge. Only a rebased
// record carries served ids: the stack the live view actually
// served when a build moved the desired stack past it. The served
// pin resolves through the same seam as the desired pin, and two
// different ordered member id lists mean the lower layers changed,
// so the reused upper may hold entries the new lowers already
// serve. Pruning is an optimization: a record without served ids
// never prunes — older records predate the fields, a dmg view is
// never rebased, and a file-path image records no pin — and a
// served stack that no longer resolves warns and skips the prune
// instead of failing the replay.
func replayPruneUpper(
	cfg config.Config,
	action session.Action,
	desired image.Target,
	warn io.Writer,
) bool {
	if action.ServedID == "" && action.ServedTip == "" {
		return false
	}
	if action.Name == "" || action.FullID == "" {
		return false
	}
	served, err := resolveStackTip(cfg, action.Name, action.ServedID, action.ServedTip)
	if err != nil {
		_, _ = fmt.Fprintf(warn,
			"warning: session: replay %s: the served stack %s no longer"+
				" resolves; the reused upper stays unpruned: %v\n",
			action.Image, describeServedPin(action), err)
		return false
	}
	return !slices.Equal(stackIDs(served), stackIDs(desired))
}

// describeServedPin renders the served pin of one recorded action for
// the prune-skip warning. A pin recorded without a tip — the
// full-id-only spelling a rebase may freeze — quotes the id alone,
// and a hand-edited record carrying only a tip quotes the tip alone;
// neither leaves a dangling "tip" word or an empty half in the
// message. Callers reach it only when one half is set.
func describeServedPin(action session.Action) string {
	switch {
	case action.ServedID == "":
		return "tip " + action.ServedTip
	case action.ServedTip == "":
		return action.ServedID
	default:
		return action.ServedID + " tip " + action.ServedTip
	}
}

// stackIDs lists the member ids of one resolved target in mount
// order, the full image root first.
func stackIDs(target image.Target) []string {
	ids := make([]string, len(target.Members))
	for i, member := range target.Members {
		ids[i] = member.ID
	}
	return ids
}

// sessionUmount tears one recorded point down through the same
// engine the umount command uses. The cascade inside one view and
// the idempotent already-unmounted answer come from the engine. The
// combination directory is recovered from the mount table entry or,
// when the overlay is already gone, from the covering session
// record.
func sessionUmount(cfg config.Config) session.UmountFunc {
	return func(mountpoint string) error {
		table, err := loadMountTable()
		if errors.Is(err, mountinfo.ErrUnavailable) && readHdiutilInfo != nil {
			return detachDiskMount(mountpoint)
		}
		if err != nil {
			return fmt.Errorf("session: %w", err)
		}
		entries := toEntries(table)
		_, err = unmountEngine(workspace.UnmountRequest{
			Dir:        mountpoint,
			Table:      entries,
			RuntimeDir: resolveCombinationRuntime(cfg, mountpoint, entries),
			Owned:      ownedLibrarySource(cfg.ImagesDir),
			Reload:     reloadEntries,
			Run:        mountRun,
			Hdiutil:    runHdiutil,
		})
		return err
	}
}

// sessionTableHeader names the short-list columns.
var sessionTableHeader = []string{"SESSION", "STATE", "MOUNTS", "CREATED", "CLOSED", "LAST ACTIVITY", "RM"}

// writeSessionList prints an aligned table: a header and one row per
// session. An empty library prints nothing.
var sessionTimeNow = time.Now

func writeSessionList(w io.Writer, summaries []session.Summary) error {
	return writeSessionListAt(w, summaries, sessionTimeNow())
}

func writeSessionListAt(w io.Writer, summaries []session.Summary, now time.Time) error {
	if len(summaries) == 0 {
		return nil
	}
	rows := make([][]string, len(summaries))
	widths := make([]int, len(sessionTableHeader))
	copy(widths, lengths(sessionTableHeader))
	for i, summary := range summaries {
		rows[i] = sessionTableCols(summary, now)
		for c, col := range rows[i] {
			if len(col) > widths[c] {
				widths[c] = len(col)
			}
		}
	}
	if err := writePadded(w, sessionTableHeader, widths, "", false); err != nil {
		return err
	}
	for _, cols := range rows {
		if err := writePadded(w, cols, widths, "", false); err != nil {
			return err
		}
	}
	return nil
}

func sessionTableCols(summary session.Summary, now time.Time) []string {
	rm := "-"
	if summary.RemoveOnClose {
		rm = "yes"
	}
	return []string{
		summary.Name,
		summary.State,
		strconv.Itoa(summary.Mounts),
		formatImageListTime(summary.Created, now),
		formatImageListTime(summary.Closed, now),
		formatSessionActivity(summary.Activity, now),
		rm,
	}
}

// sessionsDocument is the session list --json envelope.
type sessionsDocument struct {
	Sessions []sessionSummaryJSON `json:"sessions"`
}

// sessionSummaryJSON is one list entry aligned with the Summary read
// model. Times are RFC3339; a zero closed_at renders as-is, matching
// the on-disk record behavior. The schema is add-only.
type sessionSummaryJSON struct {
	sessionActivityJSON
	Name      string `json:"name"`
	State     string `json:"state"`
	Mounts    int    `json:"mounts"`
	CreatedAt string `json:"created_at"`
	ClosedAt  string `json:"closed_at"`
}

func writeSessionListJSON(w io.Writer, summaries []session.Summary) error {
	doc := sessionsDocument{Sessions: make([]sessionSummaryJSON, 0, len(summaries))}
	for _, summary := range summaries {
		doc.Sessions = append(doc.Sessions, sessionSummaryJSON{
			sessionActivityJSON: newSessionActivityJSON(summary.Activity),
			Name:                summary.Name,
			State:               summary.State,
			Mounts:              summary.Mounts,
			CreatedAt:           summary.Created.Format(time.RFC3339),
			ClosedAt:            summary.Closed.Format(time.RFC3339),
		})
	}
	return writeJSON(w, doc)
}
