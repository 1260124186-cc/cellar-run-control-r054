package main

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/backup"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/config"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/storage"
)

type restoreFlags struct {
	dataDir           string
	yes               bool
	safetyCopyPath    string
	replaceSafetyCopy bool
}

func parseRestoreFlags(arguments []string) (restoreFlags, string, error) {
	flags := flag.NewFlagSet("restore", flag.ContinueOnError)
	var values restoreFlags
	flags.StringVar(&values.dataDir, "data-dir", "", "data directory containing state.json (defaults to CELLAR_CONTROL_DATA_DIR or ./data)")
	flags.StringVar(&values.dataDir, "d", "", "alias for --data-dir")
	flags.BoolVar(&values.yes, "yes", false, "skip the interactive confirmation (use only after reviewing the scope)")
	flags.BoolVar(&values.yes, "y", false, "alias for --yes")
	flags.StringVar(&values.safetyCopyPath, "safety-copy", "", "where to keep the pre-restore snapshot (defaults to a timestamped <data-dir>/state.pre-restore-*.json)")
	flags.BoolVar(&values.replaceSafetyCopy, "replace-safety-copy", false, "replace the safety copy file if it already exists instead of refusing")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "usage: fermentctl restore [--data-dir DIR] [--safety-copy FILE] [--replace-safety-copy] [--yes] <archive>")
		flags.PrintDefaults()
	}
	if err := flags.Parse(arguments); err != nil {
		return restoreFlags{}, "", err
	}
	if flags.NArg() != 1 {
		return restoreFlags{}, "", errors.New("restore requires exactly one archive path")
	}
	return values, flags.Arg(0), nil
}

func runRestore(arguments []string, stdout, stderr io.Writer, stdin io.Reader) error {
	values, archivePath, err := parseRestoreFlags(arguments)
	if err != nil {
		return err
	}
	dataDir, err := config.ResolveDataDir(values.dataDir)
	if err != nil {
		return err
	}

	archive, err := backup.OpenArchive(archivePath)
	if err != nil {
		return fmt.Errorf("the backup cannot be trusted: %w", err)
	}
	integrity := backup.Verify(archive)
	if !integrity.Valid() {
		printInspection(stdout, integrity, false)
		return errors.New("restore aborted: the backup is incomplete or inconsistent")
	}

	dataLock, err := storage.AcquireExclusiveDataLock(dataDir)
	if err != nil {
		return err
	}
	defer dataLock.Close()
	store, err := storage.Open(dataDir)
	if err != nil {
		return fmt.Errorf("open target cellar: %w", err)
	}
	plan, err := backup.BuildPlan(integrity.Snapshot, store.CurrentSnapshot())
	if err != nil {
		return err
	}

	fmt.Fprintln(stdout, "Restore scope (nothing has been changed yet):")
	printPlan(stdout, plan)
	fmt.Fprintln(stdout)

	if !values.yes {
		answer, err := confirm(stdin, "Type 'yes' to switch the cellar to this backup state: ")
		if err != nil {
			return err
		}
		if !answer {
			fmt.Fprintln(stdout, "Restore cancelled; the current cellar data is untouched.")
			return nil
		}
	}

	result, err := backup.Apply(store, plan, backup.ApplyOptions{
		ExpectedTargetHash:  plan.TargetHash,
		SafetyCopyPath:      values.safetyCopyPath,
		OverwriteSafetyCopy: values.replaceSafetyCopy,
	})
	if err != nil {
		if errors.Is(err, storage.ErrSnapshotChanged) {
			return errors.New("restore aborted: the target snapshot changed after the scope was reviewed; rerun and confirm again")
		}
		return err
	}

	fmt.Fprintf(stdout, "Restore complete at revision %d with checksum %s.\n",
		result.Revision, result.InstalledHash)
	if result.SafetyCopyCreated {
		fmt.Fprintf(stdout, "The previous live snapshot was preserved at %s for rollback.\n",
			result.SafetyCopyPath)
	} else {
		fmt.Fprintln(stdout, "No previous snapshot existed; no safety copy was needed.")
	}
	return nil
}
