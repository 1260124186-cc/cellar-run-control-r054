package backup_test

import (
	"bytes"
	"os"
	"testing"
)

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return raw
}

func writeFile(t *testing.T, path string, raw []byte) {
	t.Helper()
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func indexOf(t *testing.T, haystack, needle []byte) int {
	t.Helper()
	index := bytes.Index(haystack, needle)
	if index < 0 {
		t.Fatalf("test payload did not contain %q", string(needle))
	}
	return index
}
