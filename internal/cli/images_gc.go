package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jiangxin/tiler/internal/image"
	"github.com/jiangxin/tiler/internal/session"
	"github.com/spf13/cobra"
)

func newImagesGCCmd() *cobra.Command {
	var rawExpire string
	var assumeYes bool
	cmd := &cobra.Command{
		Use: "gc", Short: "Preview and remove expired unreferenced image versions",
		Long: "GC lists expired image versions not referenced by sessions, while\n" +
			"preserving every ancestor needed by a retained version. It also\n" +
			"labels pending-cleanup and orphan objects. Nothing is deleted\n" +
			"until interactive input is exactly yes or --yes is supplied;\n" +
			"noninteractive input without --yes only previews the candidates.\n" +
			"Missing activity logs are initialized and retained this scan.\n" +
			"Pending cleanup runs alone; rerun GC afterward for expiration.\n" +
			"GC never removes, closes, or unmounts sessions. Retention uses\n" +
			"--expire (default 30 days), not the legacy session age settings.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return runImagesGC(cmd, rawExpire, assumeYes) },
	}
	bindExpireFlag(cmd, &rawExpire)
	cmd.Flags().BoolVar(&assumeYes, "yes", false, "remove the listed image objects without asking")
	return cmd
}

func runImagesGC(cmd *cobra.Command, rawExpire string, assumeYes bool) error {
	window, err := expireWindow(rawExpire)
	if err != nil {
		return err
	}
	cfg, ok := loadedConfig(cmd)
	if !ok {
		return errors.New("images gc: configuration is not loaded")
	}
	now := time.Now().UTC()
	coordinator := session.ReferenceCoordinator{Store: session.Store{Dir: cfg.SessionsDir, LogsDir: cfg.LogsDir}, ImagesDir: cfg.ImagesDir}
	plan, err := coordinator.PlanImageGC(now.Add(-window), nil)
	if err != nil {
		return fmt.Errorf("images gc: %w", err)
	}
	if err := writeImageGCSkips(cmd.ErrOrStderr(), plan.Versions); err != nil {
		return err
	}
	preview := imageGCPreview(plan, now, now.Add(-window))
	approved, err := (gcConfirmation{Preview: preview, In: cmd.InOrStdin(),
		Out: cmd.OutOrStdout(), ErrOut: cmd.ErrOrStderr(),
		Objects: "image objects", AssumeYes: assumeYes}).approved()
	if err != nil || !approved {
		return err
	}
	outcomes, execErr := coordinator.ExecuteImageGC(plan, nil)
	outcomes = imageGCReportedOutcomes(plan, outcomes, execErr)
	reportErr := writeImageGCReport(cmd.OutOrStdout(), outcomes, len(outcomes))
	if err := errors.Join(execErr, reportErr); err != nil {
		return fmt.Errorf("images gc: %w", err)
	}
	return nil
}

func imageGCPreview(plan session.ImageGCPlan, now, cutoff time.Time) gcPreview {
	var cleanup, expired []string
	for _, item := range plan.Cleanup.Items {
		cleanup = append(cleanup, fmt.Sprintf("  %s  %s", item.Kind, formatImageGCID(item.Name, item.ID)))
	}
	for _, versions := range plan.Versions {
		for _, candidate := range versions.Candidates {
			expired = append(expired, fmt.Sprintf("  expired  %s  last active %s  age %dd",
				formatImageGCID(versions.Name, candidate.Meta.ID), formatGCStamp(candidate.Activity.Last),
				int64(now.Sub(candidate.Activity.Last)/expireDay)))
		}
	}
	if len(cleanup) == 0 && len(expired) == 0 {
		return gcPreview{Title: fmt.Sprintf("No image objects eligible for GC before %s.", formatGCStamp(cutoff))}
	}
	notice := imageGCNotice(plan)
	if len(cleanup) == 0 {
		return gcPreview{
			Title:  fmt.Sprintf("Image GC candidates (last activity before %s):", formatGCStamp(cutoff)),
			Rows:   expired,
			Notice: notice,
		}
	}
	if len(expired) == 0 {
		return gcPreview{Title: "Image GC cleanup candidates:", Rows: cleanup, Notice: notice}
	}
	return gcPreview{
		Sections: []gcSection{
			{Title: "Image GC cleanup candidates:", Rows: cleanup},
			{Title: fmt.Sprintf("Expired image versions (last activity before %s):", formatGCStamp(cutoff)), Rows: expired},
		},
		Notice: notice,
	}
}

// formatImageGCID renders name:id and omits the colon when a cleanup journal
// has no object id.
func formatImageGCID(name, id string) string {
	if id == "" {
		return name
	}
	return name + ":" + id
}

func imageGCNotice(plan session.ImageGCPlan) string {
	notice := "Only listed image objects are eligible; sessions and external source files are preserved."
	if plan.Cleanup.Pending {
		notice += "\nPending cleanup runs alone; rerun images gc afterward to inspect expiration."
	}
	return notice
}

// imageGCReportedOutcomes gives every approved cleanup or expired row one
// outcome. A cleanup error can return before later approved rows run, and a
// new session reference returns no outcomes at all; those rows are skips so
// the summary partitions the approved list.
func imageGCReportedOutcomes(plan session.ImageGCPlan, outcomes []image.VersionGCOutcome, execErr error) []image.VersionGCOutcome {
	seen := make(map[string]int, len(outcomes))
	for _, outcome := range outcomes {
		seen[outcome.Name+"\x00"+outcome.ID]++
	}
	var missing []image.VersionGCOutcome
	record := func(name, id string) {
		key := name + "\x00" + id
		if seen[key] > 0 {
			seen[key]--
			return
		}
		missing = append(missing, skippedImageGCOutcome(name, id, execErr))
	}
	for _, item := range plan.Cleanup.Items {
		record(item.Name, item.ID)
	}
	for _, versions := range plan.Versions {
		for _, candidate := range versions.Candidates {
			record(versions.Name, candidate.Meta.ID)
		}
	}
	return append(outcomes, missing...)
}

func skippedImageGCOutcome(name, id string, execErr error) image.VersionGCOutcome {
	outcome := image.VersionGCOutcome{Name: name, ID: id, Action: "skipped", Reason: "not executed"}
	if execErr == nil {
		return outcome
	}
	if strings.Contains(execErr.Error(), fmt.Sprintf("cleanup %s:%s is referenced", name, id)) {
		outcome.Reason = "referenced"
		return outcome
	}
	outcome.Detail = execErr.Error()
	return outcome
}

func writeImageGCSkips(w io.Writer, plans []image.VersionGCPlan) error {
	for _, plan := range plans {
		for _, skip := range plan.Skips {
			if _, err := fmt.Fprintf(w, "kept %s:%s: %s (%s)\n", plan.Name, skip.ID, skip.Reason, skip.Detail); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeImageGCReport(w io.Writer, outcomes []image.VersionGCOutcome, approved int) error {
	removed, skipped := 0, 0
	for _, outcome := range outcomes {
		line := fmt.Sprintf("%s %s", outcome.Action, formatImageGCID(outcome.Name, outcome.ID))
		if outcome.Action == image.GCActionRemoved {
			removed++
		} else {
			skipped++
			line += ": " + outcome.Reason
			if outcome.Detail != "" {
				line += " (" + outcome.Detail + ")"
			}
		}
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(w, "removed %d of %d approved image objects, skipped %d\n", removed, approved, skipped)
	return err
}
