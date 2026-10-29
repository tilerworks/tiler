package cli

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/jiangxin/tiler/internal/session"
	"github.com/spf13/cobra"
)

// newSessionGCCmd registers the session expiry scan. It is the only session
// command that removes records by age, and it removes nothing before the
// listing is approved, so an old --rm record keeps waiting for a user like
// every other record.
func newSessionGCCmd() *cobra.Command {
	var (
		rawExpire string
		assumeYes bool
	)
	cmd := &cobra.Command{
		Use:   "gc",
		Short: "Remove expired idle sessions",
		Long: "GC scans every session record and lists the ones whose last\n" +
			"activity is older than --expire days, whose views are not\n" +
			"mounted, and whose lock no other process holds. The listing\n" +
			"shows the record state and the newest activity of each\n" +
			"candidate. Nothing is removed until the user answers the\n" +
			"question with yes, or until --yes is given: without --yes a\n" +
			"noninteractive run only shows the listing and removes\n" +
			"nothing. Removing a session deletes its whole directory,\n" +
			"including runtime/ and the upper data of every view; the scan\n" +
			"never unmounts anything and keeps an attached or busy session.\n" +
			"Expiry follows the activity log alone: the scan never closes\n" +
			"an active session and never ages a record by its creation\n" +
			"time. A --rm record is no exception: the flag only describes\n" +
			"what a close would have done. Each approved session is\n" +
			"rechecked against its record, its activity log, the fixed\n" +
			"cutoff, and the live mounts before it is removed, and a\n" +
			"session that changed or disappeared in the meantime is\n" +
			"reported as kept.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSessionGC(cmd, rawExpire, assumeYes)
		},
	}
	bindExpireFlag(cmd, &rawExpire)
	cmd.Flags().BoolVar(&assumeYes, "yes", false,
		"remove the listed sessions without asking")
	return cmd
}

// runSessionGC scans, confirms, and executes one session GC. The window is
// validated before the scan, so a bad --expire never walks the sessions, and
// the plan's cutoff stays fixed while the user answers.
func runSessionGC(cmd *cobra.Command, rawExpire string, assumeYes bool) error {
	window, err := expireWindow(rawExpire)
	if err != nil {
		return err
	}
	cfg, ok := loadedConfig(cmd)
	if !ok {
		return errors.New("session gc: configuration is not loaded")
	}
	engine := sessionEngine(cfg)
	plan, err := engine.PlanSessions(time.Now().Add(-window))
	if err != nil {
		return fmt.Errorf("session gc: %w", err)
	}
	if err := writeSessionGCKept(cmd.ErrOrStderr(), plan.Skips); err != nil {
		return err
	}
	confirmation := gcConfirmation{
		Preview:   sessionGCPreview(plan),
		In:        cmd.InOrStdin(),
		Out:       cmd.OutOrStdout(),
		ErrOut:    cmd.ErrOrStderr(),
		Objects:   "sessions",
		AssumeYes: assumeYes,
	}
	approved, err := confirmation.approved()
	if err != nil {
		return err
	}
	if !approved {
		return nil
	}
	report, execErr := engine.ExecSessions(plan)
	if err := writeSessionGCReport(cmd.OutOrStdout(), report, len(plan.Candidates)); err != nil {
		return err
	}
	if execErr != nil {
		return fmt.Errorf("session gc: %w", execErr)
	}
	return nil
}

// sessionGCPreview renders the listing the confirmation shows. The rows keep
// the plan's name order, so the same store always renders the same listing.
// An empty plan names the cutoff instead of offering rows, and the shared
// confirmation never asks about an empty listing.
func sessionGCPreview(plan session.GCPlan) gcPreview {
	if len(plan.Candidates) == 0 {
		return gcPreview{Title: fmt.Sprintf("No session expired before %s.",
			formatGCStamp(plan.Cutoff))}
	}
	width := 0
	for _, candidate := range plan.Candidates {
		if len(candidate.Name) > width {
			width = len(candidate.Name)
		}
	}
	rows := make([]string, len(plan.Candidates))
	for i, candidate := range plan.Candidates {
		row := fmt.Sprintf("  %-*s  %s  last active %s",
			width, candidate.Name, candidate.State, formatGCStamp(candidate.LastActive))
		if candidate.RemoveOnClose {
			row += "  --rm"
		}
		rows[i] = row
	}
	return gcPreview{
		Title: fmt.Sprintf("Expired sessions (last activity before %s):",
			formatGCStamp(plan.Cutoff)),
		Rows: rows,
		Notice: "Removing a session deletes its whole directory: the record, its\n" +
			"activity log, and runtime/, upper data of every view included.",
	}
}

// writeSessionGCKept reports the sessions a scan or an execution kept and
// why, one line each, so a user can tell an attached, busy, or unreadable
// session from a young one. The plan's keeps are diagnostic output on the
// error stream; the execution's keeps are part of the outcome report.
func writeSessionGCKept(w io.Writer, skips []session.GCSkip) error {
	for _, skip := range skips {
		if _, err := fmt.Fprintf(w, "kept %s: %s (%s)\n",
			skip.Name, skip.Reason, skip.Detail); err != nil {
			return err
		}
	}
	return nil
}

// writeSessionGCReport prints the outcome of the approved execution: one line
// per removed session, one line per session the execution kept, and a summary
// over the approved plan. Every approved candidate appears exactly once.
func writeSessionGCReport(w io.Writer, report session.GCReport, approved int) error {
	for _, name := range report.Removed {
		if _, err := fmt.Fprintf(w, "removed %s\n", name); err != nil {
			return err
		}
	}
	if err := writeSessionGCKept(w, report.Kept); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "removed %d of %d approved sessions, kept %d\n",
		len(report.Removed), approved, len(report.Kept))
	return err
}

// formatGCStamp renders one listing timestamp: UTC with second precision, the
// same spelling the other session listings use.
func formatGCStamp(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}
