package storage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenBlocksSecondProcessAndRecoversStaleLock(t *testing.T) {
	dir := t.TempDir()
	first, err := Open(dir)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if _, err := Open(filepath.Join(dir)); err == nil {
		t.Fatal("second open while the lock is held must fail")
	} else {
		t.Logf("second open correctly rejected: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	// After a clean close the lock file is removed and reopen succeeds.
	second, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen after close: %v", err)
	}
	defer second.Close()
	if err := second.Close(); err != nil {
		t.Fatalf("close second: %v", err)
	}

	// Simulate a crashed holder: lockfile remains on disk but nothing holds
	// the OS flock. Open must recover automatically.
	if err := os.WriteFile(filepath.Join(dir, LockFileName), []byte("99999\n"), 0o600); err != nil {
		t.Fatalf("write stale lock: %v", err)
	}
	recovered, err := Open(dir)
	if err != nil {
		t.Fatalf("open with stale lock must recover: %v", err)
	}
	defer recovered.Close()
}
