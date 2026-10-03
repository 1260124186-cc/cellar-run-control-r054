package backup_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/backup"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/storage"
)

func backupFixture(t *testing.T, store *storage.Store) (string, backup.Report) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "backup.json")
	result, err := backup.Create(store.Current(), backup.CreateOptions{
		OutputPath: path, Source: "test",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	report, _, err := backup.VerifyFile(result.Path)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	return path, report
}

func firstFormulaID(t *testing.T, snapshot domain.Snapshot) string {
	t.Helper()
	for id := range snapshot.Formulas {
		return id
	}
	t.Fatal("snapshot has no formulas")
	return ""
}

func TestAnalyzeConflictsAndScope(t *testing.T) {
	source := t.TempDir()
	sourceStore, _ := buildFixtureStore(t, filepath.Join(source, "data"))
	path, report := backupFixture(t, sourceStore)

	t.Run("identical snapshot", func(t *testing.T) {
		targetStore, err := storage.Open(filepath.Join(t.TempDir(), "data"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := targetStore.RestoreSnapshot(report.Snapshot); err != nil {
			t.Fatalf("seed identical target: %v", err)
		}
		_, plan, err := backup.Preflight(path, targetStore)
		if err != nil {
			t.Fatalf("preflight: %v", err)
		}
		if !plan.Identical || plan.HasChanges() || len(plan.Conflicts) != 0 {
			t.Fatalf("identical plan mismatch: %+v", plan)
		}
	})

	t.Run("empty target adds everything", func(t *testing.T) {
		targetStore, err := storage.Open(filepath.Join(t.TempDir(), "data"))
		if err != nil {
			t.Fatal(err)
		}
		_, plan, err := backup.Preflight(path, targetStore)
		if err != nil {
			t.Fatalf("preflight: %v", err)
		}
		if plan.Adds != 3 || plan.Deletes != 0 || plan.ObservationDelta != 3 {
			t.Fatalf("empty-target plan = %+v", plan)
		}
	})

	t.Run("unique key collision blocks restore", func(t *testing.T) {
		// The backup formula name is owned on the target by a different
		// formula id; RestoreSnapshot rebuilds indexes from the entity maps.
		target := domain.EmptySnapshot()
		backupFormula := report.Snapshot.Formulas[firstFormulaID(t, report.Snapshot)]
		foreign := backupFormula.Clone()
		foreign.ID = "formula_foreign000000000000001"
		foreign.Style = "foreign"
		target.Formulas[foreign.ID] = foreign

		targetStore, err := storage.Open(filepath.Join(t.TempDir(), "data"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := targetStore.RestoreSnapshot(target); err != nil {
			t.Fatalf("seed conflict target: %v", err)
		}
		_, plan, err := backup.Preflight(path, targetStore)
		if err != nil {
			t.Fatalf("preflight: %v", err)
		}
		if len(plan.Conflicts) == 0 {
			t.Fatal("expected key conflict to block restore")
		}
		if err := backup.RequireNoConflicts(plan); err == nil {
			t.Fatal("RequireNoConflicts must reject the plan")
		}
	})

	t.Run("target only rows are deleted by full replacement", func(t *testing.T) {
		// A second fixture carries entirely different random ids and codes.
		otherDir := t.TempDir()
		otherStore, _ := buildFixtureStore(t, filepath.Join(otherDir, "data"))
		targetStore, err := storage.Open(filepath.Join(t.TempDir(), "data"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := targetStore.RestoreSnapshot(otherStore.Current()); err != nil {
			t.Fatalf("seed target: %v", err)
		}
		_, plan, err := backup.Preflight(path, targetStore)
		if err != nil {
			t.Fatalf("preflight: %v", err)
		}
		if plan.Deletes != 3 || plan.Adds != 3 {
			t.Fatalf("replacement scope = adds %d deletes %d, want 3/3",
				plan.Adds, plan.Deletes)
		}
	})
}

func TestApplyIsAtomicAndPreservesSafetyCopy(t *testing.T) {
	source := t.TempDir()
	sourceStore, _ := buildFixtureStore(t, filepath.Join(source, "data"))
	path, report := backupFixture(t, sourceStore)

	targetDir := filepath.Join(t.TempDir(), "data")
	targetStore, err := storage.Open(targetDir)
	if err != nil {
		t.Fatal(err)
	}
	// Seed the target with an identical snapshot first so state.json and a
	// safety copy exist; then diverge it by restoring a second fixture.
	if _, err := targetStore.RestoreSnapshot(report.Snapshot); err != nil {
		t.Fatalf("seed identical: %v", err)
	}
	otherStore, _ := buildFixtureStore(t, filepath.Join(t.TempDir(), "other"))
	diverged := otherStore.Current()
	// Rename the other fixture's unique keys so rows coexist without a
	// hard key collision; restore must still replace the whole snapshot.
	for id, formula := range diverged.Formulas {
		formula.Name = "Other Beer"
		diverged.Formulas[id] = formula
	}
	for id, vessel := range diverged.Vessels {
		vessel.Code = "FV-OTHER"
		diverged.Vessels[id] = vessel
	}
	for id, run := range diverged.Runs {
		run.Code = "RUNOTHER1"
		diverged.Runs[id] = run
	}
	if _, err := targetStore.RestoreSnapshot(diverged); err != nil {
		t.Fatalf("seed diverged target: %v", err)
	}

	result, err := backup.Apply(path, targetStore)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if result.BackupRevision != report.Snapshot.Revision {
		t.Fatalf("restored revision %d, want %d",
			result.BackupRevision, report.Snapshot.Revision)
	}
	if result.SafetyCopyPath == "" {
		t.Fatal("expected a pre-restore safety copy path")
	}
	if _, err := os.Stat(filepath.Join(targetDir, storage.PreRestoreStateFileName)); err != nil {
		t.Fatalf("named safety copy missing: %v", err)
	}

	_, plan, err := backup.Preflight(path, targetStore)
	if err != nil {
		t.Fatalf("post-restore preflight: %v", err)
	}
	if !plan.Identical {
		t.Fatalf("post-restore snapshot differs from backup: %+v", plan)
	}
}

func TestApplyRejectsTamperedFile(t *testing.T) {
	source := t.TempDir()
	sourceStore, _ := buildFixtureStore(t, filepath.Join(source, "data"))
	path, _ := backupFixture(t, sourceStore)

	targetDir := filepath.Join(t.TempDir(), "data")
	targetStore, err := storage.Open(targetDir)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	tampered := make([]byte, len(raw))
	copy(tampered, raw)
	idx := indexOf(t, tampered, []byte("1.011"))
	tampered[idx+4] = '9'
	if err := os.WriteFile(path, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := backup.Apply(path, targetStore); err == nil {
		t.Fatal("tampered backup must not restore")
	}
	if _, err := os.Stat(filepath.Join(targetDir, "state.json")); !os.IsNotExist(err) {
		t.Fatalf("failed restore must not create state.json, err=%v", err)
	}
}
