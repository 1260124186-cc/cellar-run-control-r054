package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/backup"
)

type backupCommandOptions struct {
	dataDir   string
	output    string
	outputDir string
	source    string
	note      string
	force     bool
	dryRun    bool
}

func runBackupCommand(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("backup", flag.ContinueOnError)
	flags.SetOutput(stderr)
	options := backupCommandOptions{}
	hostname, _ := os.Hostname()
	flags.StringVar(&options.dataDir, "data-dir", "",
		"cellar data directory (defaults to "+dataDirEnvVar+" or ./data)")
	flags.StringVar(&options.output, "output", "",
		"backup file path (defaults to a deterministic name in the output directory)")
	flags.StringVar(&options.outputDir, "output-dir", ".",
		"directory for the default backup file name")
	flags.StringVar(&options.source, "source", hostname,
		"machine label recorded in the backup manifest")
	flags.StringVar(&options.note, "note", "",
		"optional operator note recorded in the backup manifest")
	flags.BoolVar(&options.force, "force", false,
		"overwrite an existing different backup file")
	flags.BoolVar(&options.dryRun, "dry-run", false,
		"list the inventory that would be backed up without writing a file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("backup takes no positional arguments")
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
	snapshot := store.Current()

	inventory := backup.BuildInventory(snapshot)
	printInventory(stdout, inventory)

	if options.dryRun {
		fmt.Fprintln(stdout, "Dry run: no backup file was written.")
		return nil
	}
	if inventory.Counts.Runs == 0 && inventory.Counts.Formulas == 0 &&
		inventory.Counts.Vessels == 0 {
		fmt.Fprintln(stdout, "The cellar is empty; the backup will contain an empty snapshot.")
	}

	result, err := backup.Create(snapshot, backup.CreateOptions{
		OutputPath: options.output,
		OutputDir:  options.outputDir,
		Source:     options.source,
		Note:       options.note,
		Overwrite:  options.force,
	})
	if err != nil {
		return err
	}
	payloadDigest, envelopeDigest := backup.DigestSummary(result.Envelope)
	if result.Skipped {
		fmt.Fprintf(stdout,
			"Backup unchanged: %s already contains this exact cellar state and passed verification.\n",
			result.Path)
	} else if result.Overwrote {
		fmt.Fprintf(stdout, "Replaced previous backup file: %s\n", result.Path)
	} else {
		fmt.Fprintf(stdout, "Backup written: %s\n", result.Path)
	}
	fmt.Fprintf(stdout, "Snapshot revision %d, payload sha256 %s\n",
		result.Envelope.Manifest.Revision, payloadDigest)
	fmt.Fprintf(stdout, "Envelope sha256 %s, %d bytes\n", envelopeDigest, result.Bytes)
	fmt.Fprintln(stdout, "Verify later with: fermentctl verify-backup --file "+result.Path)
	return nil
}
