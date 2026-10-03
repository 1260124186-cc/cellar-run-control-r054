package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/backup"
)

type restoreCommandOptions struct {
	dataDir string
	file    string
	yes     bool
	dryRun  bool
}

func runRestoreCommand(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("restore", flag.ContinueOnError)
	flags.SetOutput(stderr)
	options := restoreCommandOptions{}
	flags.StringVar(&options.dataDir, "data-dir", "",
		"cellar data directory (defaults to "+dataDirEnvVar+" or ./data)")
	flags.StringVar(&options.file, "file", "", "backup file to restore (required)")
	flags.BoolVar(&options.yes, "yes", false,
		"approve the overwrite without an interactive prompt")
	flags.BoolVar(&options.dryRun, "dry-run", false,
		"verify the backup and show the overwrite plan without changing data")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("restore takes no positional arguments")
	}
	if strings.TrimSpace(options.file) == "" {
		return fmt.Errorf("restore requires --file <backup>")
	}

	dataDir, err := resolveDataDir(flags)
	if err != nil {
		return err
	}
	store, err := openStore(dataDir)
	if err != nil {
		return err
	}
	defer closeStore(store)

	fmt.Fprintln(stdout, "STOP the cellar service (fermentctl serve) before continuing.")
	fmt.Fprintln(stdout, "A restore replaces state.json while another process writes at your own risk.")

	report, plan, err := backup.Preflight(options.file, store)
	if err != nil {
		return err
	}
	printRestorePlan(stdout, report, plan)

	if plan.Identical {
		fmt.Fprintln(stdout, "Target already matches this backup exactly; no changes are needed.")
		return nil
	}
	if err := backup.RequireNoConflicts(plan); err != nil {
		return err
	}

	if options.dryRun {
		fmt.Fprintln(stdout, "Dry run: no data was changed.")
		return nil
	}

	approved := options.yes
	if !approved {
		if !backup.IsInteractive(stdin, stdout) {
			return errors.New(
				"restore would change data but no terminal is available for confirmation; " +
					"rerun with --yes to approve non-interactively")
		}
		answer, err := backup.Confirm(stdin, stdout,
			"Type 'yes' to replace the current cellar state with the backup:", "yes")
		if err != nil {
			return err
		}
		approved = answer
	}
	if !approved {
		return errors.New("restore was not confirmed; current cellar data is untouched")
	}

	result, err := backup.Apply(options.file, store)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Restore complete at revision %d.\n", result.BackupRevision)
	if result.SafetyCopyPath != "" {
		fmt.Fprintf(stdout,
			"The previous state was preserved at %s (copy it back over state.json to roll back).\n",
			result.SafetyCopyPath)
	} else {
		fmt.Fprintln(stdout, "No previous state.json existed, so no safety copy was created.")
	}
	fmt.Fprintln(stdout, "Start the cellar service and verify the data before reopening operations.")
	return nil
}

func printRestorePlan(out io.Writer, report backup.Report, plan backup.Plan) {
	payloadDigest, envelopeDigest := backup.DigestSummary(report.Envelope)
	fmt.Fprintln(out, "Backup verification: OK")
	fmt.Fprintf(out, "  created:           %s\n", backup.CreatedAt(report))
	fmt.Fprintf(out, "  snapshot updated:  %s\n", backup.SnapshotAt(report))
	fmt.Fprintf(out, "  revision:          %d\n", report.Snapshot.Revision)
	fmt.Fprintf(out, "  payload sha256:    %s\n", payloadDigest)
	fmt.Fprintf(out, "  envelope sha256:   %s\n", envelopeDigest)
	fmt.Fprintf(out, "  contents:          %s, %s, %s, %s\n",
		plural(report.Counts.Formulas, "formula"),
		plural(report.Counts.Vessels, "vessel"),
		plural(report.Counts.Runs, "run"),
		plural(report.Counts.Observations, "observation"))

	if len(plan.Conflicts) > 0 {
		fmt.Fprintln(out, "Conflicts (restore blocked):")
		for _, conflict := range plan.Conflicts {
			fmt.Fprintf(out, "  - %s\n", conflict.Message)
		}
	} else {
		fmt.Fprintln(out, "Overwrite scope:")
		if plan.Identical {
			fmt.Fprintln(out, "  target and backup are identical.")
		}
		for _, change := range plan.Changes {
			if change.Action == backup.ActionIdentical {
				continue
			}
			line := fmt.Sprintf("  %-9s %-11s %s", change.Action, change.Scope, change.ID)
			if change.Code != "" {
				line += " (" + change.Code + ")"
			}
			if change.Detail != "" {
				line += " - " + change.Detail
			}
			fmt.Fprintln(out, line)
		}
		fmt.Fprintf(out, "Summary: %s added, %s overwritten, %s deleted; observations net %+d.\n",
			plural(plan.Adds, "row"), plural(plan.Overwrites, "row"),
			plural(plan.Deletes, "row"), plan.ObservationDelta)
	}
}
