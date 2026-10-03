//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package storage

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

const lockFileName = "cellar.lock"

// dataLock is an advisory flock on <data-dir>/cellar.lock. The running service
// and read-only offline commands hold a shared lock; a restore replaces the
// snapshot and therefore requires an exclusive lock, which fails immediately
// while the service is still serving.
type dataLock struct {
	handle *os.File
}

func acquireDataLock(dataDir string, exclusive bool) (io.Closer, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	handle, err := os.OpenFile(filepath.Join(dataDir, lockFileName),
		os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open data lock: %w", err)
	}
	how := syscall.LOCK_SH
	if exclusive {
		how = syscall.LOCK_EX
	}
	if err := syscall.Flock(int(handle.Fd()), how|syscall.LOCK_NB); err != nil {
		_ = handle.Close()
		if exclusive {
			return nil, fmt.Errorf("the cellar data directory is locked by another process; stop the service before restoring: %w", err)
		}
		return nil, fmt.Errorf("the cellar data directory is locked by a restore in progress: %w", err)
	}
	return &dataLock{handle: handle}, nil
}

// AcquireSharedDataLock locks the data directory for concurrent readers and
// for the running service.
func AcquireSharedDataLock(dataDir string) (io.Closer, error) {
	return acquireDataLock(dataDir, false)
}

// AcquireExclusiveDataLock locks the data directory for a snapshot restore.
func AcquireExclusiveDataLock(dataDir string) (io.Closer, error) {
	return acquireDataLock(dataDir, true)
}

func (l *dataLock) Close() error {
	unlockErr := syscall.Flock(int(l.handle.Fd()), syscall.LOCK_UN)
	closeErr := l.handle.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}
