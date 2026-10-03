package backup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/storage"
)

// CreateResult reports exactly what a Create did, so running the same backup
// twice always has an unambiguous outcome.
type CreateResult struct {
	Path         string
	Archive      Archive
	Bytes        int
	Reused       bool
	ReusedPath   string
	Overwrote    bool
	ExistingKind string
}

// CreateOptions control archive creation.
type CreateOptions struct {
	// Output is a file path or a directory. Empty selects a timestamped name
	// in the current directory.
	Output string
	// Force overwrites an existing output file whose content differs.
	Force bool
	// Now stamps the archive and default filename.
	Now time.Time
}

// Create writes a complete, checksummed backup of snapshot.
//
// If the output path already holds a byte-identical archive it is reused and
// nothing is rewritten. If another archive in the same directory already
// contains the identical payload, the caller is told where it lives and no new
// file is created. A different file at the output path is rejected unless
// Force is set; with Force it is atomically replaced.
func Create(snapshot domain.Snapshot, options CreateOptions) (CreateResult, error) {
	createdAt := options.Now.UTC()
	archive, encoded, err := NewArchive(snapshot, createdAt)
	if err != nil {
		return CreateResult{}, err
	}
	outputPath, err := filepath.Abs(resolveOutputPath(options.Output, createdAt))
	if err != nil {
		return CreateResult{}, fmt.Errorf("resolve backup path: %w", err)
	}

	if raw, statErr := os.ReadFile(outputPath); statErr == nil {
		if bytesEqual(raw, encoded) {
			return CreateResult{
				Path:         outputPath,
				Archive:      archive,
				Bytes:        len(encoded),
				Reused:       true,
				ReusedPath:   outputPath,
				ExistingKind: "identical",
			}, nil
		}
		if existing, decodeErr := DecodeArchive(raw); decodeErr == nil &&
			existing.Checksum == archive.Checksum {
			return CreateResult{
				Path:         outputPath,
				Archive:      archive,
				Bytes:        len(encoded),
				Reused:       true,
				ReusedPath:   outputPath,
				ExistingKind: "same-payload-different-envelope",
			}, nil
		}
		if !options.Force {
			return CreateResult{}, fmt.Errorf(
				"%s already exists with different content; use --force to replace it",
				outputPath)
		}
		if err := writeBackupFile(outputPath, encoded); err != nil {
			return CreateResult{}, err
		}
		if err := assertArchiveAt(outputPath, archive.Checksum); err != nil {
			return CreateResult{}, err
		}
		return CreateResult{
			Path:         outputPath,
			Archive:      archive,
			Bytes:        len(encoded),
			Overwrote:    true,
			ExistingKind: "different",
		}, nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return CreateResult{}, fmt.Errorf("inspect backup path: %w", statErr)
	}

	if twinPath, found := findPayloadTwin(filepath.Dir(outputPath), archive.Checksum, outputPath); found {
		return CreateResult{
			Path:         outputPath,
			Archive:      archive,
			Bytes:        len(encoded),
			Reused:       true,
			ReusedPath:   twinPath,
			ExistingKind: "identical-payload",
		}, nil
	}

	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return CreateResult{}, fmt.Errorf("create backup directory: %w", err)
	}
	if err := writeBackupFile(outputPath, encoded); err != nil {
		return CreateResult{}, err
	}
	if err := assertArchiveAt(outputPath, archive.Checksum); err != nil {
		return CreateResult{}, err
	}
	return CreateResult{
		Path:    outputPath,
		Archive: archive,
		Bytes:   len(encoded),
	}, nil
}

func writeBackupFile(path string, encoded []byte) error {
	return storage.AtomicWriteFile(path, 0o600, func(handle *os.File) error {
		if _, err := handle.Write(encoded); err != nil {
			return fmt.Errorf("write backup: %w", err)
		}
		return nil
	})
}

// findPayloadTwin scans a directory for another valid archive embedding the
// same payload hash. Archives from other systems may carry a different created
// time but identical cellar data, and that is still a repeated backup.
func findPayloadTwin(directory, checksum string, exclude string) (string, bool) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return "", false
	}
	candidates := make([]string, 0)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		candidates = append(candidates, filepath.Join(directory, entry.Name()))
	}
	// Deterministic preference: sort by path.
	for i := 1; i < len(candidates); i++ {
		for j := i; j > 0 && candidates[j-1] > candidates[j]; j-- {
			candidates[j-1], candidates[j] = candidates[j], candidates[j-1]
		}
	}
	for _, candidate := range candidates {
		if candidate == exclude {
			continue
		}
		archive, err := OpenArchive(candidate)
		if err != nil {
			continue
		}
		if archive.Checksum == checksum {
			return candidate, true
		}
	}
	return "", false
}

func assertArchiveAt(path, expectedChecksum string) error {
	archive, err := OpenArchive(path)
	if err != nil {
		return fmt.Errorf("verify written backup: %w", err)
	}
	if archive.Checksum != expectedChecksum {
		return errors.New("written backup checksum does not match the requested snapshot")
	}
	return nil
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}
