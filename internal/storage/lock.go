package storage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// LockFileName is the process lock held while a data directory is open.
const LockFileName = ".cellar-control.lock"

// acquireDataDirLock takes an exclusive advisory lock for the lifetime of the
// process. It coordinates the HTTP service and the offline backup/restore
// commands so a restore cannot replace state.json while a server is running.
//
// The lock is a lockfile carrying the holder PID. A stale lock from a dead
// process is recovered automatically. The returned file must stay open for
// the duration of the lock; closing it releases the OS-level flock.
func acquireDataDirLock(directory string) (*os.File, error) {
	path := filepath.Join(directory, LockFileName)
	handle, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open data directory lock: %w", err)
	}

	if err := syscall.Flock(int(handle.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = handle.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			holder := readLockHolder(path)
			return nil, fmt.Errorf(
				"data directory %s is locked by another cellar process (%s); "+
					"stop the service before restore/backup operations", directory, holder)
		}
		return nil, fmt.Errorf("acquire data directory lock: %w", err)
	}

	if err := handle.Truncate(0); err != nil {
		_ = syscall.Flock(int(handle.Fd()), syscall.LOCK_UN)
		_ = handle.Close()
		return nil, fmt.Errorf("record data directory lock: %w", err)
	}
	if _, err := fmt.Fprintf(handle, "%d\n", os.Getpid()); err != nil {
		_ = syscall.Flock(int(handle.Fd()), syscall.LOCK_UN)
		_ = handle.Close()
		return nil, fmt.Errorf("record data directory lock: %w", err)
	}
	_ = handle.Sync()
	return handle, nil
}

func readLockHolder(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil || strings.TrimSpace(string(raw)) == "" {
		return "unknown pid"
	}
	if pid, convErr := strconv.Atoi(strings.TrimSpace(string(raw))); convErr == nil {
		return fmt.Sprintf("pid %d", pid)
	}
	return "unknown pid"
}
