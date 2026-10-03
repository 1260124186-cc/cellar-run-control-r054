package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
)

func TestRestoreSnapshotPreservesLiveStateOnRejection(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	original := domain.EmptySnapshot()
	original.Revision = 7
	original.Formulas["formula_live0000000000000001"] = domain.Formula{
		ID: "formula_live0000000000000001", Name: "Live", Style: "ale", State: domain.FormulaDraft,
	}
	safety, err := store.RestoreSnapshot(original)
	if err != nil {
		t.Fatalf("seed restore: %v", err)
	}
	if safety != "" {
		t.Fatal("seeding an empty data dir must not create a safety copy")
	}

	bad := domain.EmptySnapshot()
	bad.Revision = -1
	if _, err := store.RestoreSnapshot(bad); err == nil {
		t.Fatal("negative revision candidate must be rejected")
	}
	if got := store.Current().Revision; got != 7 {
		t.Fatalf("revision after rejected restore = %d, want 7", got)
	}
	if _, err := os.Stat(filepath.Join(dir, PreRestoreStateFileName)); !os.IsNotExist(err) {
		t.Fatal("a rejected restore must not leave a safety copy behind")
	}
}

func TestRestoreSnapshotSafetyCopyMatchesPreviousState(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	previous := domain.EmptySnapshot()
	previous.Revision = 3
	previous.Formulas["formula_prev0000000000000001"] = domain.Formula{
		ID: "formula_prev0000000000000001", Name: "Previous", Style: "ale", State: domain.FormulaDraft,
	}
	if _, err := store.RestoreSnapshot(previous); err != nil {
		t.Fatalf("seed previous: %v", err)
	}

	next := previous.Clone()
	next.Revision = 4
	next.Formulas["formula_next0000000000000002"] = domain.Formula{
		ID: "formula_next0000000000000002", Name: "Next", Style: "ale", State: domain.FormulaDraft,
	}
	safety, err := store.RestoreSnapshot(next)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if safety != filepath.Join(dir, PreRestoreStateFileName) {
		t.Fatalf("safety path = %q", safety)
	}

	raw, err := os.ReadFile(safety)
	if err != nil {
		t.Fatalf("read safety copy: %v", err)
	}
	var preserved domain.Snapshot
	if err := json.Unmarshal(raw, &preserved); err != nil {
		t.Fatalf("safety copy is not valid JSON: %v", err)
	}
	if preserved.Revision != 3 {
		t.Fatalf("safety copy revision = %d, want 3", preserved.Revision)
	}
	if _, ok := preserved.Formulas["formula_next0000000000000002"]; ok {
		t.Fatal("safety copy must contain the previous state, not the new rows")
	}
	if got := store.Current().Revision; got != 4 {
		t.Fatalf("live revision = %d, want 4", got)
	}

	// Reopening the directory must load the new snapshot successfully.
	reopened, err := openWithoutProcessLock(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if reopened.Revision() != 4 {
		t.Fatalf("reopened revision = %d, want 4", reopened.Revision())
	}
}
