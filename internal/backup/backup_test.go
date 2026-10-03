package backup_test

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/backup"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/clock"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/service"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/storage"
)

// buildFixtureStore drives the real service through a full workflow so the
// fixture snapshot includes an approved formula, a cleaning vessel, a
// completed run, and three observations.
func buildFixtureStore(t *testing.T, dataDir string) (*storage.Store, domain.FermentationRun) {
	t.Helper()
	store, err := storage.Open(dataDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	fixed := clock.NewFixed(time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC))
	app := service.New(store, fixed)

	formula, err := app.CreateFormula(service.CreateFormulaInput{
		Name:                  "Fixture Pils",
		Style:                 "pils",
		TargetOriginalGravity: 1.050,
		TargetFinalGravity:    1.010,
		MaxFermentationTempC:  20,
		MinimumDays:           7,
		MaximumDays:           21,
	})
	if err != nil {
		t.Fatalf("create formula: %v", err)
	}
	if _, err := app.SetFormulaSteps(formula.ID, service.SetFormulaStepsInput{
		Steps: []domain.FormulaStep{
			{Sequence: 1, Label: "Primary", TemperatureC: 12, HoldMinutes: 720},
		},
	}); err != nil {
		t.Fatalf("set steps: %v", err)
	}
	if _, err := app.ApproveFormula(formula.ID, service.VersionInput{}); err != nil {
		t.Fatalf("approve: %v", err)
	}
	vessel, err := app.CreateVessel(service.CreateVesselInput{
		Code: "FV-FIX", CapacityL: 1000,
	})
	if err != nil {
		t.Fatalf("create vessel: %v", err)
	}
	run, err := app.CreateRun(service.CreateRunInput{
		Code: "RUNFIX1", FormulaID: formula.ID, PlannedVolumeL: 500,
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if _, _, err := app.ReserveRun(run.ID, service.ReserveRunInput{VesselID: vessel.ID}); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if _, _, err := app.StartRun(run.ID, service.VersionInput{}); err != nil {
		t.Fatalf("start: %v", err)
	}
	start := fixed.Now()
	for index, gravity := range []float64{1.050, 1.020, 1.011} {
		fixed.Advance(time.Hour)
		if _, err := app.AppendObservation(run.ID, service.ObservationInput{
			Sequence:     index + 1,
			Gravity:      gravity,
			TemperatureC: 13,
			ObservedAt:   start.Add(time.Duration(index+1) * time.Hour),
		}); err != nil {
			t.Fatalf("observation %d: %v", index+1, err)
		}
	}
	if _, err := app.BeginConditioning(run.ID, service.VersionInput{}); err != nil {
		t.Fatalf("conditioning: %v", err)
	}
	completed, vesselResult, err := app.CompleteRun(run.ID, service.VersionInput{})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if vesselResult.State != domain.VesselCleaning {
		t.Fatalf("vessel state = %s, want cleaning", vesselResult.State)
	}
	return store, completed
}

func TestCreateVerifyAndIdempotency(t *testing.T) {
	source := t.TempDir()
	store, completed := buildFixtureStore(t, filepath.Join(source, "data"))
	snapshot := store.Current()

	outDir := t.TempDir()
	first, err := backup.Create(snapshot, backup.CreateOptions{
		OutputDir: outDir, Source: "unit-test",
	})
	if err != nil {
		t.Fatalf("create backup: %v", err)
	}
	if first.Skipped {
		t.Fatal("first backup must not be reported as skipped")
	}
	if first.Inventory.Counts.Observations != 3 {
		t.Fatalf("observation count = %d, want 3", first.Inventory.Counts.Observations)
	}
	cleaning := 0
	for _, item := range first.Inventory.Items {
		if item.Kind == backup.KindCleaning {
			cleaning++
		}
	}
	if cleaning != 1 {
		t.Fatalf("cleaning facts = %d, want 1", cleaning)
	}

	report, _, err := backup.VerifyFile(first.Path)
	if err != nil {
		t.Fatalf("verify written backup: %v", err)
	}
	if report.Counts.Observations != 3 {
		t.Fatalf("verified observations = %d", report.Counts.Observations)
	}
	restoredRun := report.Snapshot.Runs[completed.ID]
	if restoredRun.State != domain.RunCompleted || len(restoredRun.Observations) != 3 {
		t.Fatalf("restored run mismatch: state=%s observations=%d",
			restoredRun.State, len(restoredRun.Observations))
	}

	// Repeating the same backup must be a verified no-op with identical bytes.
	second, err := backup.Create(snapshot, backup.CreateOptions{
		OutputDir: outDir, Source: "unit-test",
	})
	if err != nil {
		t.Fatalf("repeat backup: %v", err)
	}
	if !second.Skipped {
		t.Fatal("identical repeat backup must report skipped")
	}
	if second.Envelope.EnvelopeSHA256 != first.Envelope.EnvelopeSHA256 {
		t.Fatal("repeated backup produced a different envelope digest")
	}
}

func TestExplicitOutputCollisionRequiresForce(t *testing.T) {
	source := t.TempDir()
	store, _ := buildFixtureStore(t, filepath.Join(source, "data"))
	outDir := t.TempDir()
	target := filepath.Join(outDir, "fixed.json")

	if _, err := backup.Create(store.Current(), backup.CreateOptions{
		OutputPath: target, Source: "first",
	}); err != nil {
		t.Fatalf("create first: %v", err)
	}

	// A different snapshot written to the same explicit path must be blocked.
	changed := store.Current()
	changed.Revision = changed.Revision + 99
	if _, err := backup.Create(changed, backup.CreateOptions{
		OutputPath: target, Source: "second",
	}); err == nil {
		t.Fatal("different content at an existing path must be blocked without force")
	}

	// Forced replacement succeeds and the new file verifies.
	forced, err := backup.Create(changed, backup.CreateOptions{
		OutputPath: target, Source: "second", Overwrite: true,
	})
	if err != nil {
		t.Fatalf("forced create: %v", err)
	}
	if !forced.Overwrote {
		t.Fatal("forced replacement must report overwrite")
	}
	if _, _, err := backup.VerifyFile(target); err != nil {
		t.Fatalf("forced file must verify: %v", err)
	}
}

func TestPayloadTamperingRejected(t *testing.T) {
	source := t.TempDir()
	store, _ := buildFixtureStore(t, filepath.Join(source, "data"))
	outDir := t.TempDir()
	created, err := backup.Create(store.Current(), backup.CreateOptions{
		OutputDir: outDir, Source: "test",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	raw := readFile(t, created.Path)
	tampered := make([]byte, len(raw))
	copy(tampered, raw)
	// Change a gravity digit inside the compact payload region.
	idx := indexOf(t, tampered, []byte("1.011"))
	tampered[idx+4] = '9'
	writeFile(t, created.Path, tampered)

	if _, _, err := backup.VerifyFile(created.Path); err == nil {
		t.Fatal("tampered payload must fail verification")
	}
}

func TestManifestTamperingRejected(t *testing.T) {
	source := t.TempDir()
	store, _ := buildFixtureStore(t, filepath.Join(source, "data"))
	outDir := t.TempDir()
	created, err := backup.Create(store.Current(), backup.CreateOptions{
		OutputDir: outDir, Source: "test",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	raw := readFile(t, created.Path)
	// Change the revision inside the manifest only.
	target := `"snapshot_revision": 12`
	idx := bytes.Index(raw, []byte(target))
	if idx < 0 {
		t.Fatalf("manifest revision marker not found; raw=%s", raw)
	}
	tampered := make([]byte, len(raw))
	copy(tampered, raw)
	tampered[idx+len(target)-1] = '9'
	writeFile(t, created.Path, tampered)

	if _, _, err := backup.VerifyFile(created.Path); err == nil {
		t.Fatal("manifest revision tampering must fail verification")
	}
}

func TestEmptyCellarBackupRestores(t *testing.T) {
	emptyDir := filepath.Join(t.TempDir(), "data")
	store, err := storage.Open(emptyDir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	outDir := t.TempDir()
	created, err := backup.Create(store.Current(), backup.CreateOptions{
		OutputDir: outDir, Source: "test",
	})
	if err != nil {
		t.Fatalf("empty backup: %v", err)
	}
	report, _, err := backup.VerifyFile(created.Path)
	if err != nil {
		t.Fatalf("verify empty backup: %v", err)
	}
	if report.Counts != (backup.Counts{}) {
		t.Fatalf("empty counts = %+v", report.Counts)
	}
}
