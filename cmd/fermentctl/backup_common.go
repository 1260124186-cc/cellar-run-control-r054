package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/backup"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/storage"
)

const dataDirEnvVar = "CELLAR_CONTROL_DATA_DIR"

func resolveDataDir(flags *flag.FlagSet) (string, error) {
	value := strings.TrimSpace(flags.Lookup("data-dir").Value.String())
	if value == "" {
		value = strings.TrimSpace(os.Getenv(dataDirEnvVar))
	}
	if value == "" {
		value = "./data"
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("resolve data directory: %w", err)
	}
	return filepath.Clean(absolute), nil
}

func openStore(dataDir string) (*storage.Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	store, err := storage.Open(dataDir)
	if err != nil {
		return nil, err
	}
	return store, nil
}

func closeStore(store *storage.Store) {
	if store != nil {
		_ = store.Close()
	}
}

func printInventory(out io.Writer, inventory backup.Inventory) {
	byKind := map[backup.Kind]int{}
	for _, item := range inventory.Items {
		byKind[item.Kind]++
	}
	fmt.Fprintln(out, "Backup inventory:")
	for _, item := range inventory.Items {
		switch item.Kind {
		case backup.KindFormula:
			fmt.Fprintf(out, "  formula     %-22s state=%-9s %s (%s)\n",
				item.ID, item.State, item.Name, item.Detail)
		case backup.KindVessel:
			runPart := "-"
			if item.RunCode != "" {
				runPart = item.RunCode
			}
			fmt.Fprintf(out, "  vessel      %-22s code=%-12s state=%-9s run=%s %s\n",
				item.ID, item.Code, item.State, runPart, item.Detail)
		case backup.KindRun:
			fmt.Fprintf(out, "  run         %-22s code=%-12s state=%-12s (%s)\n",
				item.ID, item.Code, item.State, item.Detail)
		case backup.KindCleaning:
			fmt.Fprintf(out, "  cleaning    vessel=%-16s run=%-12s %s\n",
				item.Code, item.RunCode, item.Detail)
		case backup.KindObservation:
			fmt.Fprintf(out, "  observation run=%-12s seq=%-3d %s\n",
				item.RunCode, item.Sequence, item.Detail)
		}
	}
	fmt.Fprintf(out, "Totals: %s, %s, %s, %s; cleaning facts: %d\n",
		plural(inventory.Counts.Formulas, "formula"),
		plural(inventory.Counts.Vessels, "vessel"),
		plural(inventory.Counts.Runs, "run"),
		plural(inventory.Counts.Observations, "observation"),
		byKind[backup.KindCleaning])
}

func plural(count int, word string) string {
	if count == 1 {
		return fmt.Sprintf("1 %s", word)
	}
	return fmt.Sprintf("%d %ss", count, word)
}
