package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func writeSnapshot(path string, snapshot any) error {
	return AtomicWriteFile(path, 0o600, func(handle *os.File) error {
		encoder := json.NewEncoder(handle)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(snapshot); err != nil {
			return fmt.Errorf("encode state: %w", err)
		}
		return nil
	})
}

// AtomicWriteFile streams content into a temporary file in the destination
// directory, commits it durably, and renames it over the target. The target is
// never observed partially written, even across process interruption.
func AtomicWriteFile(path string, mode os.FileMode, write func(*os.File) error) error {
	directory := filepath.Dir(path)
	handle, err := os.CreateTemp(directory, ".cellar-write-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	tempName := handle.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tempName)
		}
	}()

	if err := write(handle); err != nil {
		_ = handle.Close()
		return err
	}
	if err := handle.Sync(); err != nil {
		_ = handle.Close()
		return fmt.Errorf("commit file bytes: %w", err)
	}
	if err := handle.Close(); err != nil {
		return fmt.Errorf("close temporary file: %w", err)
	}
	if err := os.Chmod(tempName, mode); err != nil {
		return fmt.Errorf("set file permissions: %w", err)
	}
	if err := os.Rename(tempName, path); err != nil {
		return fmt.Errorf("replace file: %w", err)
	}
	if err := syncDirectory(directory); err != nil {
		return err
	}
	removeTemp = false
	return nil
}

func syncDirectory(directory string) error {
	handle, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open directory for durability: %w", err)
	}
	defer handle.Close()
	if err := handle.Sync(); err != nil {
		return fmt.Errorf("commit directory entry: %w", err)
	}
	return nil
}
