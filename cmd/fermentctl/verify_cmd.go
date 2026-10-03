package main

import (
	"flag"
	"fmt"
	"io"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/backup"
)

type verifyCommandOptions struct {
	file string
}

func runVerifyBackupCommand(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("verify-backup", flag.ContinueOnError)
	flags.SetOutput(stderr)
	options := verifyCommandOptions{}
	flags.StringVar(&options.file, "file", "", "backup file to verify (required)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("verify-backup takes no positional arguments")
	}
	if options.file == "" {
		return fmt.Errorf("verify-backup requires --file <backup>")
	}

	report, _, err := backup.VerifyFile(options.file)
	if err != nil {
		return err
	}
	payloadDigest, envelopeDigest := backup.DigestSummary(report.Envelope)
	fmt.Fprintln(stdout, "Backup verification: OK")
	fmt.Fprintf(stdout, "  file:              %s\n", options.file)
	fmt.Fprintf(stdout, "  format version:    %d\n", report.Envelope.Manifest.FormatVersion)
	fmt.Fprintf(stdout, "  created:           %s\n", backup.CreatedAt(report))
	fmt.Fprintf(stdout, "  source:            %s\n", report.Envelope.Manifest.Source)
	fmt.Fprintf(stdout, "  snapshot updated:  %s\n", backup.SnapshotAt(report))
	fmt.Fprintf(stdout, "  revision:          %d\n", report.Snapshot.Revision)
	fmt.Fprintf(stdout, "  contents:          %s, %s, %s, %s\n",
		plural(report.Counts.Formulas, "formula"),
		plural(report.Counts.Vessels, "vessel"),
		plural(report.Counts.Runs, "run"),
		plural(report.Counts.Observations, "observation"))
	fmt.Fprintf(stdout, "  payload sha256:    %s\n", payloadDigest)
	fmt.Fprintf(stdout, "  envelope sha256:   %s\n", envelopeDigest)
	return nil
}
