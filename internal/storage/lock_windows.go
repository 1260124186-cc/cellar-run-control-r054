//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd)

package storage

import "io"

// File locking is only wired up on POSIX systems in this build. On Windows the
// offline commands and the service must not share a data directory at the same
// time, as documented for the restore workflow.
func AcquireSharedDataLock(_ string) (io.Closer, error) {
	return noopCloser{}, nil
}

func AcquireExclusiveDataLock(_ string) (io.Closer, error) {
	return noopCloser{}, nil
}

type noopCloser struct{}

func (noopCloser) Close() error { return nil }
