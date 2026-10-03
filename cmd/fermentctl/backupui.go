package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/backup"
)

func newTabWriter(output io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(output, 0, 0, 2, ' ', 0)
}

func printPreview(output io.Writer, preview backup.Preview) {
	fmt.Fprintf(output, "Backup contents preview (source revision %d):\n", preview.Snapshot.Revision)

	fmt.Fprintf(output, "\nFormulas: %d\n", preview.Manifest.Formulas)
	if len(preview.Formulas) > 0 {
		tab := newTabWriter(output)
		fmt.Fprintln(tab, "  ID\tNAME\tSTYLE\tSTATE\tSTEPS\tVERSION")
		for _, formula := range preview.Formulas {
			fmt.Fprintf(tab, "  %s\t%s\t%s\t%s\t%d\t%d\n",
				formula.ID, formula.Name, formula.Style, formula.State, formula.Steps, formula.Version)
		}
		tab.Flush()
	}

	fmt.Fprintf(output, "\nVessels: %d\n", preview.Manifest.Vessels)
	if len(preview.Vessels) > 0 {
		tab := newTabWriter(output)
		fmt.Fprintln(tab, "  ID\tCODE\tCAPACITY_L\tSTATE\tACTIVE_RUN\tVERSION")
		for _, vessel := range preview.Vessels {
			activeRun := "-"
			if vessel.ActiveRunID != "" {
				activeRun = vessel.ActiveRunID
			}
			fmt.Fprintf(tab, "  %s\t%s\t%.0f\t%s\t%s\t%d\n",
				vessel.ID, vessel.Code, vessel.CapacityL, vessel.State, activeRun, vessel.Version)
		}
		tab.Flush()
	}

	fmt.Fprintf(output, "\nRuns: %d (gravity observations: %d)\n",
		preview.Manifest.Runs, preview.Manifest.Observations)
	if len(preview.Runs) > 0 {
		tab := newTabWriter(output)
		fmt.Fprintln(tab, "  ID\tCODE\tFORMULA\tVESSEL\tSTATE\tOBSERVATIONS\tVERSION")
		for _, run := range preview.Runs {
			vessel := "-"
			if run.VesselID != "" {
				vessel = run.VesselID
			}
			fmt.Fprintf(tab, "  %s\t%s\t%s\t%s\t%s\t%d\t%d\n",
				run.ID, run.Code, run.FormulaID, vessel, run.State, run.Observations, run.Version)
		}
		tab.Flush()
	}

	fmt.Fprintf(output, "\nCleaning facts: %d\n", preview.Manifest.Cleaning)
	if len(preview.Cleaning) > 0 {
		tab := newTabWriter(output)
		fmt.Fprintln(tab, "  VESSEL\tCODE\tRUN\tRUN_CODE\tRUN_STATE")
		for _, fact := range preview.Cleaning {
			fmt.Fprintf(tab, "  %s\t%s\t%s\t%s\t%s\n",
				fact.VesselID, fact.VesselCode, fact.RunID, fact.RunCode, fact.RunState)
		}
		tab.Flush()
	}
}

func printCreateResult(output io.Writer, result backup.CreateResult) {
	created := result.Archive.CreatedAt
	fmt.Fprintf(output, "Backup created at: %s\n", created.Format(time.RFC3339))
	fmt.Fprintf(output, "Archive: %s (%d bytes)\n", result.Path, result.Bytes)
	fmt.Fprintf(output, "Checksum %s: %s\n", result.Archive.ChecksumAlgorithm, result.Archive.Checksum)
	fmt.Fprintf(output, "Contents: %d formulas, %d vessels, %d runs (%d observations), %d cleaning facts\n",
		result.Archive.Manifest.Formulas,
		result.Archive.Manifest.Vessels,
		result.Archive.Manifest.Runs,
		result.Archive.Manifest.Observations,
		result.Archive.Manifest.Cleaning)
	fmt.Fprintf(output, "Source snapshot revision: %d\n", result.Archive.SourceRevision)

	switch {
	case result.Reused && result.ReusedPath == result.Path && result.ExistingKind == "identical":
		fmt.Fprintln(output, "Result: archive already exists with identical bytes; nothing was rewritten (idempotent).")
	case result.Reused && result.ReusedPath == result.Path:
		fmt.Fprintln(output, "Result: archive already exists and embeds the identical snapshot; nothing was rewritten.")
	case result.Reused:
		fmt.Fprintf(output, "Result: identical payload already backed up at %s; no new file was written.\n",
			result.ReusedPath)
	case result.Overwrote:
		fmt.Fprintln(output, "Result: replaced a different archive at this path (--force).")
	default:
		fmt.Fprintln(output, "Result: new archive written.")
	}
}

func printInspection(output io.Writer, report backup.IntegrityReport, quiet bool) {
	archive := report.Archive
	fmt.Fprintf(output, "Archive created: %s\n", archive.CreatedAt.Format(time.RFC3339))
	fmt.Fprintf(output, "Source revision: %d (updated %s)\n",
		archive.SourceRevision, archive.SourceUpdatedAt.Format(time.RFC3339))
	fmt.Fprintf(output, "Checksum %s: %s\n", archive.ChecksumAlgorithm, archive.Checksum)
	fmt.Fprintf(output, "Integrity: %s\n", report.Status)
	if quiet {
		for _, problem := range report.Errors {
			fmt.Fprintf(output, "  - %s\n", problem)
		}
		return
	}
	if len(report.Errors) > 0 {
		fmt.Fprintln(output, "Problems:")
		for _, problem := range report.Errors {
			fmt.Fprintf(output, "  - %s\n", problem)
		}
	}
	if report.Valid() {
		printPreview(output, backup.BuildPreview(report.Snapshot))
	}
}

func printPlan(output io.Writer, plan backup.Plan) {
	counts := plan.Counts()
	fmt.Fprintf(output, "Backup integrity: valid\n")
	fmt.Fprintf(output, "Backup checksum: %s\n", plan.BackupHash)
	fmt.Fprintf(output, "Target checksum: %s\n", plan.TargetHash)
	fmt.Fprintln(output)
	printScopeLine(output, "Formulas", counts.Formulas)
	printChangeTable(output, plan.Formulas)
	printScopeLine(output, "Vessels", counts.Vessels)
	printChangeTable(output, plan.Vessels)
	printScopeLine(output, "Runs", counts.Runs)
	printChangeTable(output, plan.Runs)
	fmt.Fprintf(output, "Gravity observations after switch: %d\n", counts.Observations)

	if len(plan.Conflicts) == 0 {
		fmt.Fprintln(output, "\nUnique-key conflicts: none")
	} else {
		fmt.Fprintf(output, "\nUnique-key conflicts: %d (all resolved by the full-snapshot switch)\n",
			len(plan.Conflicts))
		tab := newTabWriter(output)
		fmt.Fprintln(tab, "  KIND\tDOMAIN\tKEY\tBACKUP_ID\tTARGET_ID\tRESOLUTION")
		for _, conflict := range plan.Conflicts {
			fmt.Fprintf(tab, "  %s\t%s\t%s\t%s\t%s\t%s\n",
				conflict.Kind, conflict.Domain, conflict.Key,
				conflict.BackupID, conflict.TargetID, conflict.Resolution)
		}
		tab.Flush()
	}
}

func printScopeLine(output io.Writer, label string, counts map[backup.ChangeKind]int) {
	fmt.Fprintf(output, "%s: %d added, %d overwritten, %d removed, %d already identical\n",
		label,
		counts[backup.ChangeAdded],
		counts[backup.ChangeOverwritten],
		counts[backup.ChangeRemoved],
		counts[backup.ChangeIdentical])
}

func printChangeTable(output io.Writer, changes []backup.EntityChange) {
	relevant := make([]backup.EntityChange, 0, len(changes))
	for _, change := range changes {
		if change.Kind != backup.ChangeIdentical {
			relevant = append(relevant, change)
		}
	}
	if len(relevant) == 0 {
		return
	}
	tab := newTabWriter(output)
	fmt.Fprintln(tab, "  ACTION\tID\tLABEL\tDETAIL")
	for _, change := range relevant {
		fmt.Fprintf(tab, "  %s\t%s\t%s\t%s\n", change.Kind, change.ID, change.Label, change.Detail)
	}
	tab.Flush()
}

// confirm asks for explicit typed confirmation. Non-interactive input and any
// answer other than "yes" aborts.
func confirm(reader io.Reader, prompt string) (bool, error) {
	fmt.Fprint(os.Stdout, prompt)
	scanner := bufio.NewScanner(reader)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return false, err
		}
		return false, nil
	}
	return strings.TrimSpace(strings.ToLower(scanner.Text())) == "yes", nil
}
