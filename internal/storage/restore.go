package storage

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
)

// PreRestoreStateFileName is the durable safety copy of the live snapshot
// that is written immediately before a restore replaces state.json.
const PreRestoreStateFileName = "state.pre-restore.json"

// Current returns a defensive copy of the in-memory snapshot.
func (s *Store) Current() domain.Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshot.Clone()
}

// rebuildAndNormalize derives indexes from the entity collections and then
// runs the full snapshot validation. Used by restore, where the payload's
// redundant index maps are never trusted as authored.
func rebuildAndNormalize(value domain.Snapshot) (domain.Snapshot, error) {
	value.NameIndex = make(map[string]string, len(value.Formulas))
	for id, formula := range value.Formulas {
		value.NameIndex[domain.NameKey(formula.Name)] = id
	}
	value.VesselIndex = make(map[string]string, len(value.Vessels))
	for id, vessel := range value.Vessels {
		value.VesselIndex[vessel.Code] = id
	}
	value.RunIndex = make(map[string]string, len(value.Runs))
	for id, run := range value.Runs {
		value.RunIndex[run.Code] = id
	}
	return NormalizeSnapshot(value)
}

// RestoreSnapshot fully replaces the stored snapshot with candidate.
//
// Indexes are rebuilt from the entity collections (so a backup's redundant
// index maps cannot diverge from the rows they describe), and the result is
// fully validated before anything durable happens. The replacement is staged
// to a durable temporary file first. The current state.json (when present) is
// copied durably to state.pre-restore.json before the replacement is renamed
// into place, so an interruption at any point leaves either the previous live
// snapshot or a fully written new one. The in-memory snapshot is updated only
// after the durable switch succeeds.
func (s *Store) RestoreSnapshot(candidate domain.Snapshot) (string, error) {
	normalized, err := rebuildAndNormalize(candidate)
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	directory := filepath.Dir(s.path)
	stagePath, stageCleanup, err := stageFile(directory, ".state-restore-*.tmp")
	if err != nil {
		return "", err
	}
	defer stageCleanup()
	if err := writeSnapshotAt(stagePath, normalized); err != nil {
		return "", err
	}

	safetyCopy := ""
	if _, statErr := os.Stat(s.path); statErr == nil {
		preserved, err := preserveCurrentSnapshot(directory, s.path)
		if err != nil {
			return "", err
		}
		safetyCopy = preserved
	} else if !os.IsNotExist(statErr) {
		return "", fmt.Errorf("inspect current state: %w", statErr)
	}

	if err := os.Rename(stagePath, s.path); err != nil {
		return "", fmt.Errorf("replace state: %w", err)
	}
	if err := syncDirectory(directory); err != nil {
		return "", err
	}
	s.snapshot = normalized
	return safetyCopy, nil
}

func preserveCurrentSnapshot(directory, livePath string) (string, error) {
	target := filepath.Join(directory, PreRestoreStateFileName)
	tempPath, cleanup, err := stageFile(directory, ".pre-restore-*.tmp")
	if err != nil {
		return "", err
	}
	defer cleanup()

	if err := copyFileDurably(livePath, tempPath); err != nil {
		return "", err
	}
	if err := os.Rename(tempPath, target); err != nil {
		return "", fmt.Errorf("publish pre-restore safety copy: %w", err)
	}
	if err := syncDirectory(directory); err != nil {
		return "", err
	}
	return target, nil
}

func copyFileDurably(sourcePath, destinationPath string) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("read current state for safety copy: %w", err)
	}
	defer source.Close()

	destination, err := os.OpenFile(destinationPath, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open safety copy: %w", err)
	}
	if _, err := io.Copy(destination, source); err != nil {
		_ = destination.Close()
		return fmt.Errorf("copy current state: %w", err)
	}
	if err := destination.Sync(); err != nil {
		_ = destination.Close()
		return fmt.Errorf("commit safety copy bytes: %w", err)
	}
	if err := destination.Close(); err != nil {
		return fmt.Errorf("close safety copy: %w", err)
	}
	return nil
}
