package main

import (
	"flag"
	"fmt"
	"io"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/backup"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/clock"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/config"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/storage"
)

type backupFlags struct {
	dataDir string
	output  string
	force   bool
}

func parseBackupFlags(arguments []string) (backupFlags, error) {
	flags := flag.NewFlagSet("backup", flag.ContinueOnError)
	var values backupFlags
	flags.StringVar(&values.dataDir, "data-dir", "", "data directory containing state.json (defaults to CELLAR_CONTROL_DATA_DIR or ./data)")
	flags.StringVar(&values.dataDir, "d", "", "alias for --data-dir")
	flags.StringVar(&values.output, "output", "", "backup file path or directory (defaults to a timestamped file)")
	flags.StringVar(&values.output, "o", "", "alias for --output")
	flags.BoolVar(&values.force, "force", false, "replace an existing output file that contains different data")
	flags.BoolVar(&values.force, "f", false, "alias for --force")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "usage: fermentctl backup [--data-dir DIR] [--output FILE|DIR] [--force]")
		flags.PrintDefaults()
	}
	if err := flags.Parse(arguments); err != nil {
		return backupFlags{}, err
	}
	if flags.NArg() != 0 {
		return backupFlags{}, fmt.Errorf("backup takes no positional arguments")
	}
	return values, nil
}

func runBackup(arguments []string, stdout, stderr io.Writer, stdin io.Reader) error {
	values, err := parseBackupFlags(arguments)
	if err != nil {
		return err
	}
	dataDir, err := config.ResolveDataDir(values.dataDir)
	if err != nil {
		return err
	}
	dataLock, err := storage.AcquireSharedDataLock(dataDir)
	if err != nil {
		return err
	}
	defer dataLock.Close()
	store, err := storage.Open(dataDir)
	if err != nil {
		return err
	}

	snapshot := store.CurrentSnapshot()
	preview := backup.BuildPreview(snapshot)
	printPreview(stdout, preview)
	fmt.Fprintln(stdout)

	result, err := backup.Create(snapshot, backup.CreateOptions{
		Output: values.output,
		Force:  values.force,
		Now:    clock.System{}.Now(),
	})
	if err != nil {
		return err
	}
	printCreateResult(stdout, result)
	return nil
}
