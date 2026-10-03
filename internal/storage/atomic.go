package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func writeSnapshot(path string, snapshot any) error {
	directory := filepath.Dir(path)
	tempPath, cleanup, err := stageFile(directory, ".state-*.tmp")
	if err != nil {
		return err
	}
	defer cleanup()
	if err := writeSnapshotAt(tempPath, snapshot); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replace state: %w", err)
	}
	if err := syncDirectory(directory); err != nil {
		return err
	}
	return nil
}

// stageFile creates a closed temporary file inside directory and returns its
// path plus a cleanup helper that removes the temp file if it was not renamed
// into place.
func stageFile(directory, pattern string) (string, func(), error) {
	handle, err := os.CreateTemp(directory, pattern)
	if err != nil {
		return "", nil, fmt.Errorf("create temporary file: %w", err)
	}
	tempName := handle.Name()
	if err := handle.Close(); err != nil {
		_ = os.Remove(tempName)
		return "", nil, fmt.Errorf("close temporary file: %w", err)
	}
	return tempName, func() {
		// After a successful rename the path no longer exists, so Remove is a
		// no-op; otherwise it cleans up an uncommitted temporary file.
		_ = os.Remove(tempName)
	}, nil
}

func writeSnapshotAt(path string, snapshot any) error {
	handle, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open staged state: %w", err)
	}
	encoder := json.NewEncoder(handle)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(snapshot); err != nil {
		_ = handle.Close()
		return fmt.Errorf("encode state: %w", err)
	}
	if err := handle.Sync(); err != nil {
		_ = handle.Close()
		return fmt.Errorf("commit state bytes: %w", err)
	}
	if err := handle.Close(); err != nil {
		return fmt.Errorf("close staged state: %w", err)
	}
	return nil
}

func syncDirectory(directory string) error {
	handle, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open data directory for sync: %w", err)
	}
	defer handle.Close()
	if err := handle.Sync(); err != nil {
		return fmt.Errorf("sync data directory: %w", err)
	}
	return nil
}
