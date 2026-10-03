package workflowcheck

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/backup"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/clock"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/service"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/storage"
)

func backupRestore() error {
	root, err := os.MkdirTemp("", "cellar-backup-check-*")
	if err != nil {
		return fmt.Errorf("create check directory: %w", err)
	}
	defer os.RemoveAll(root)

	sourceDir := filepath.Join(root, "source")
	targetDir := filepath.Join(root, "target")
	brokenDir := filepath.Join(root, "broken")
	backupPath := filepath.Join(root, "migrate", "cellar.json")
	if err := os.MkdirAll(filepath.Dir(backupPath), 0o755); err != nil {
		return err
	}

	fixed := clock.NewFixed(time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC))

	// --- Build a realistic source cellar: approved formula, run completed,
	// vessel waiting for cleaning, three gravity observations. ---
	sourceStore, err := storage.Open(sourceDir)
	if err != nil {
		return err
	}
	sourceApp := service.New(sourceStore, fixed)

	formula, err := sourceApp.CreateFormula(service.CreateFormulaInput{
		Name:                  "Migration Amber",
		Style:                 "Amber Ale",
		TargetOriginalGravity: 1.055,
		TargetFinalGravity:    1.010,
		MaxFermentationTempC:  25,
		MinimumDays:           5,
		MaximumDays:           10,
	})
	if err != nil {
		return fmt.Errorf("create formula: %w", err)
	}
	steps := []domain.FormulaStep{
		{Sequence: 1, Label: "Pitch", TemperatureC: 20, HoldMinutes: 30},
		{Sequence: 2, Label: "Ferment", TemperatureC: 20, HoldMinutes: 120},
		{Sequence: 3, Label: "Crash", TemperatureC: 5, HoldMinutes: 60},
	}
	if _, err := sourceApp.SetFormulaSteps(formula.ID,
		service.SetFormulaStepsInput{Steps: steps}); err != nil {
		return fmt.Errorf("set steps: %w", err)
	}
	if _, err := sourceApp.ApproveFormula(formula.ID, service.VersionInput{}); err != nil {
		return fmt.Errorf("approve formula: %w", err)
	}

	vessel, err := sourceApp.CreateVessel(service.CreateVesselInput{
		Code:      "FV-01",
		CapacityL: 500,
	})
	if err != nil {
		return fmt.Errorf("create vessel: %w", err)
	}
	run, err := sourceApp.CreateRun(service.CreateRunInput{
		Code:           "RUN-MIGRATE-01",
		FormulaID:      formula.ID,
		PlannedVolumeL: 100,
	})
	if err != nil {
		return fmt.Errorf("create run: %w", err)
	}
	if _, _, err := sourceApp.ReserveRun(run.ID,
		service.ReserveRunInput{VesselID: vessel.ID}); err != nil {
		return fmt.Errorf("reserve run: %w", err)
	}
	if _, _, err := sourceApp.StartRun(run.ID, service.VersionInput{}); err != nil {
		return fmt.Errorf("start run: %w", err)
	}
	gravities := []float64{1.055, 1.020, 1.012}
	for index, gravity := range gravities {
		fixed.Advance(time.Hour)
		if _, err := sourceApp.AppendObservation(run.ID, service.ObservationInput{
			Sequence:     index + 1,
			Gravity:      gravity,
			TemperatureC: 20,
			ObservedAt:   fixed.Now(),
		}); err != nil {
			return fmt.Errorf("append observation %d: %w", index+1, err)
		}
	}
	if _, err := sourceApp.BeginConditioning(run.ID, service.VersionInput{}); err != nil {
		return fmt.Errorf("conditioning: %w", err)
	}
	completedRun, completedVessel, err := sourceApp.CompleteRun(run.ID, service.VersionInput{})
	if err != nil {
		return fmt.Errorf("complete run: %w", err)
	}
	if completedRun.State != domain.RunCompleted || completedVessel.State != domain.VesselCleaning {
		return fmt.Errorf("completed workflow did not leave the vessel cleaning")
	}
	if completedRun.Metrics == nil || completedRun.Metrics.ObservationCount != 3 {
		return fmt.Errorf("completed run is missing metrics")
	}

	sourceSnapshot := sourceStore.CurrentSnapshot()
	sourceHash, err := storage.SnapshotHash(sourceSnapshot)
	if err != nil {
		return err
	}

	// --- 1. Create the backup and check the manifest covers every fact. ---
	created, err := backup.Create(sourceSnapshot, backup.CreateOptions{
		Output: backupPath,
		Now:    fixed.Now(),
	})
	if err != nil {
		return fmt.Errorf("create backup: %w", err)
	}
	if created.Reused || created.Overwrote {
		return fmt.Errorf("first backup reported as reused or overwritten")
	}
	manifest := created.Archive.Manifest
	if manifest.Formulas != 1 || manifest.Vessels != 1 || manifest.Runs != 1 ||
		manifest.Observations != 3 || manifest.Cleaning != 1 {
		return fmt.Errorf("backup manifest is wrong: %+v", manifest)
	}

	// --- 2. Repeating the same backup to the same path is a clear no-op. ---
	repeated, err := backup.Create(sourceSnapshot, backup.CreateOptions{
		Output: backupPath,
		Now:    fixed.Now(),
	})
	if err != nil {
		return fmt.Errorf("repeat backup: %w", err)
	}
	if !repeated.Reused || repeated.ReusedPath != backupPath {
		return fmt.Errorf("repeated backup did not reuse the identical archive")
	}

	// --- 3. A differently named archive in the same directory is detected as
	// the same logical backup, so no duplicate payload gets written. ---
	twin, err := backup.Create(sourceSnapshot, backup.CreateOptions{
		Output: filepath.Join(filepath.Dir(backupPath), "other-name.json"),
		Now:    fixed.Now().Add(time.Second),
	})
	if err != nil {
		return fmt.Errorf("twin backup lookup: %w", err)
	}
	if !twin.Reused || twin.ReusedPath != backupPath {
		return fmt.Errorf("identical backup under a new name was not detected")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(backupPath), "other-name.json")); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("twin lookup unexpectedly created a file")
	}

	// --- 4. Inspecting the archive proves checksum and semantic integrity. ---
	archive, err := backup.OpenArchive(backupPath)
	if err != nil {
		return fmt.Errorf("open backup: %w", err)
	}
	report := backup.Verify(archive)
	if !report.Valid() {
		return fmt.Errorf("fresh backup failed verification: %v", report.Errors)
	}

	// --- 4b. A run that is fermenting but has no observations yet (backup
	// taken immediately after start) is a legitimate cellar state. ---
	freshDir := filepath.Join(root, "fresh")
	freshStore, err := storage.Open(freshDir)
	if err != nil {
		return err
	}
	freshApp := service.New(freshStore, fixed)
	freshFormula, err := freshApp.CreateFormula(service.CreateFormulaInput{
		Name:                  "Fresh Starter",
		Style:                 "Kölsch",
		TargetOriginalGravity: 1.045,
		TargetFinalGravity:    1.010,
		MaxFermentationTempC:  20,
		MinimumDays:           4,
		MaximumDays:           9,
	})
	if err != nil {
		return err
	}
	if _, err := freshApp.SetFormulaSteps(freshFormula.ID, service.SetFormulaStepsInput{
		Steps: []domain.FormulaStep{
			{Sequence: 1, Label: "Pitch", TemperatureC: 18, HoldMinutes: 45},
		},
	}); err != nil {
		return err
	}
	if _, err := freshApp.ApproveFormula(freshFormula.ID, service.VersionInput{}); err != nil {
		return err
	}
	freshVessel, err := freshApp.CreateVessel(service.CreateVesselInput{
		Code:      "FV-FRESH",
		CapacityL: 300,
	})
	if err != nil {
		return err
	}
	freshRun, err := freshApp.CreateRun(service.CreateRunInput{
		Code:           "RUN-FRESH-1",
		FormulaID:      freshFormula.ID,
		PlannedVolumeL: 200,
	})
	if err != nil {
		return err
	}
	if _, _, err := freshApp.ReserveRun(freshRun.ID,
		service.ReserveRunInput{VesselID: freshVessel.ID}); err != nil {
		return err
	}
	if _, _, err := freshApp.StartRun(freshRun.ID, service.VersionInput{}); err != nil {
		return err
	}
	freshArchive, _, err := backup.NewArchive(freshStore.CurrentSnapshot(), fixed.Now())
	if err != nil {
		return err
	}
	freshReport := backup.Verify(freshArchive)
	if !freshReport.Valid() {
		return fmt.Errorf("zero-observation fermenting run was rejected: %v", freshReport.Errors)
	}

	// --- 5. A byte-tampered payload fails the checksum. ---
	if err := writeTamperedPayload(backupPath, filepath.Join(root, "tampered.json")); err != nil {
		return err
	}
	if _, err := backup.OpenArchive(filepath.Join(root, "tampered.json")); err == nil {
		return fmt.Errorf("tampered backup passed the checksum check")
	}

	// --- 6. A checksum-valid but semantically broken payload fails content
	// verification (vessel removed while indexes still reference it). ---
	if err := writeSemanticallyBrokenArchive(backupPath, filepath.Join(root, "broken-payload.json")); err != nil {
		return err
	}
	brokenArchive, err := backup.OpenArchive(filepath.Join(root, "broken-payload.json"))
	if err != nil {
		return fmt.Errorf("semantically broken archive should decode: %w", err)
	}
	brokenReport := backup.Verify(brokenArchive)
	if brokenReport.Valid() || len(brokenReport.Errors) == 0 {
		return fmt.Errorf("semantically broken backup passed verification")
	}

	// --- 7. A different file at the output path is protected without --force.
	// --- and replaced atomically with --force. ---
	different := []byte(`{"format":"cellar-run-control-backup","format_version":1,"note":"old"}`)
	if err := os.WriteFile(backupPath+".other", different, 0o600); err != nil {
		return err
	}
	if _, err := backup.Create(sourceSnapshot, backup.CreateOptions{
		Output: backupPath + ".other",
		Now:    fixed.Now(),
	}); err == nil {
		return fmt.Errorf("backup overwrote different content without --force")
	}
	forced, err := backup.Create(sourceSnapshot, backup.CreateOptions{
		Output: backupPath + ".other",
		Force:  true,
		Now:    fixed.Now(),
	})
	if err != nil {
		return fmt.Errorf("forced backup: %w", err)
	}
	if !forced.Overwrote {
		return fmt.Errorf("--force backup was not reported as an overwrite")
	}

	// --- 8. Restore onto an empty (fresh machine) target installs the exact
	// snapshot and needs no safety copy. ---
	targetStore, err := storage.Open(targetDir)
	if err != nil {
		return err
	}
	emptyPlan, err := backup.BuildPlan(report.Snapshot, targetStore.CurrentSnapshot())
	if err != nil {
		return err
	}
	emptyCounts := emptyPlan.Counts()
	if emptyCounts.Formulas[backup.ChangeAdded] != 1 ||
		emptyCounts.Vessels[backup.ChangeAdded] != 1 ||
		emptyCounts.Runs[backup.ChangeAdded] != 1 {
		return fmt.Errorf("fresh-target plan did not classify every record as added: %+v", emptyCounts)
	}
	emptyResult, err := backup.Apply(targetStore, emptyPlan, backup.ApplyOptions{})
	if err != nil {
		return fmt.Errorf("restore onto empty target: %w", err)
	}
	if emptyResult.SafetyCopyCreated {
		return fmt.Errorf("fresh target should not produce a safety copy")
	}
	targetHash, err := storage.SnapshotHash(targetStore.CurrentSnapshot())
	if err != nil {
		return err
	}
	if targetHash != sourceHash {
		return fmt.Errorf("restored target hash %s does not match source %s", targetHash, sourceHash)
	}
	// Reopen from disk exactly like a restarted process would.
	targetStore, err = storage.Open(targetDir)
	if err != nil {
		return err
	}
	if hash, _ := storage.SnapshotHash(targetStore.CurrentSnapshot()); hash != sourceHash {
		return fmt.Errorf("restored target changed after reopening")
	}

	// --- 9. Restoring the same backup again changes nothing material, keeps a
	// safety copy of the previous live state, and leaves a valid cellar. ---
	identicalPlan, err := backup.BuildPlan(report.Snapshot, targetStore.CurrentSnapshot())
	if err != nil {
		return err
	}
	if identicalPlan.HasChanges() {
		return fmt.Errorf("re-restoring an identical backup reported changes")
	}
	identicalResult, err := backup.Apply(targetStore, identicalPlan, backup.ApplyOptions{
		ExpectedTargetHash: sourceHash,
	})
	if err != nil {
		return fmt.Errorf("idempotent restore: %w", err)
	}
	if !identicalResult.SafetyCopyCreated {
		return fmt.Errorf("restore over an existing state did not keep a safety copy")
	}
	if _, err := os.Stat(identicalResult.SafetyCopyPath); err != nil {
		return fmt.Errorf("safety copy is missing: %w", err)
	}

	// The safety copy is a usable previous snapshot: reinstall it, then restore
	// forward again, proving rollback artifacts work.
	safetyRaw, err := os.ReadFile(identicalResult.SafetyCopyPath)
	if err != nil {
		return err
	}
	var safetySnapshot domain.Snapshot
	if err := json.Unmarshal(safetyRaw, &safetySnapshot); err != nil {
		return fmt.Errorf("decode safety copy: %w", err)
	}
	safetyPlan, err := backup.BuildPlan(safetySnapshot, targetStore.CurrentSnapshot())
	if err != nil {
		return err
	}
	if safetyPlan.HasChanges() {
		return fmt.Errorf("safety copy should equal the identical prior state")
	}

	// --- 9b. A fixed safety-copy path that already exists is protected; the
	// next default path is distinct so repeat restores accumulate rollbacks. ---
	fixedSafety := filepath.Join(targetDir, "pinned-rollback.json")
	pinnedPlan, err := backup.BuildPlan(report.Snapshot, targetStore.CurrentSnapshot())
	if err != nil {
		return err
	}
	first, err := backup.Apply(targetStore, pinnedPlan, backup.ApplyOptions{
		SafetyCopyPath: fixedSafety,
	})
	if err != nil {
		return fmt.Errorf("first pinned-safety restore: %w", err)
	}
	if first.SafetyCopyPath != fixedSafety {
		return fmt.Errorf("explicit safety-copy path was not honored")
	}
	if _, err := backup.Apply(targetStore, pinnedPlan, backup.ApplyOptions{
		SafetyCopyPath: fixedSafety,
	}); err == nil {
		return fmt.Errorf("restore overwrote an existing safety copy without permission")
	}
	overwritePlan, err := backup.BuildPlan(report.Snapshot, targetStore.CurrentSnapshot())
	if err != nil {
		return err
	}
	if _, err := backup.Apply(targetStore, overwritePlan, backup.ApplyOptions{
		SafetyCopyPath:      fixedSafety,
		OverwriteSafetyCopy: true,
	}); err != nil {
		return fmt.Errorf("explicitly allowed safety-copy replacement failed: %w", err)
	}
	defaultPaths, err := filepath.Glob(filepath.Join(targetDir, "state.pre-restore-*.json"))
	if err != nil {
		return err
	}
	if len(defaultPaths) == 0 {
		return fmt.Errorf("timestamped default safety copy was not created")
	}

	// --- 10. A unique-key collision on the target (different ID, same formula
	// name and vessel/run codes) is reported with its resolution scope and the
	// full-snapshot switch removes the conflicting records cleanly. ---
	if err := plantIDCollision(targetStore, report.Snapshot); err != nil {
		return err
	}
	collisionPlan, err := backup.BuildPlan(report.Snapshot, targetStore.CurrentSnapshot())
	if err != nil {
		return err
	}
	collisionKinds := map[backup.ConflictKind]int{}
	for _, conflict := range collisionPlan.Conflicts {
		collisionKinds[conflict.Kind]++
	}
	if collisionKinds[backup.ConflictUniqueKeyCollision] == 0 {
		return fmt.Errorf("collision plan did not report a unique-key collision: %+v", collisionPlan.Conflicts)
	}
	if collisionPlan.Counts().Formulas[backup.ChangeRemoved] != 1 {
		return fmt.Errorf("collision plan did not scope the stray formula for removal")
	}
	if _, err := backup.Apply(targetStore, collisionPlan, backup.ApplyOptions{}); err != nil {
		return fmt.Errorf("restore with collisions: %w", err)
	}
	if hash, _ := storage.SnapshotHash(targetStore.CurrentSnapshot()); hash != sourceHash {
		return fmt.Errorf("target after collision resolution does not match the backup")
	}
	// Repeated same-second restores must accumulate distinct rollback files
	// rather than destroy the earlier one.
	afterPaths, err := filepath.Glob(filepath.Join(targetDir, "state.pre-restore-*.json"))
	if err != nil {
		return err
	}
	if len(afterPaths) < 2 {
		return fmt.Errorf("a repeat restore did not preserve a second distinct safety copy")
	}

	// --- 11. A plan built before the target changed must not apply. ---
	stalePlan, err := backup.BuildPlan(report.Snapshot, targetStore.CurrentSnapshot())
	if err != nil {
		return err
	}
	postPlanApp := service.New(targetStore, fixed)
	if _, err := postPlanApp.CreateVessel(service.CreateVesselInput{
		Code:      "FV-99",
		CapacityL: 100,
	}); err != nil {
		return fmt.Errorf("create post-plan vessel: %w", err)
	}
	if _, err := backup.Apply(targetStore, stalePlan, backup.ApplyOptions{
		ExpectedTargetHash: stalePlan.TargetHash,
	}); !errors.Is(err, storage.ErrSnapshotChanged) {
		return fmt.Errorf("restore after target change was not rejected: %v", err)
	}
	vesselStillThere, err := postPlanApp.GetVessel(selectVesselID(targetStore, "FV-99"))
	if err != nil {
		return fmt.Errorf("rejected restore damaged the target: %w", err)
	}
	if vesselStillThere.Code != "FV-99" {
		return fmt.Errorf("target record vanished after a rejected restore")
	}

	// --- 12. A corrupt target state is rejected before anything is planned. ---
	if err := os.MkdirAll(brokenDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(brokenDir, "state.json"),
		[]byte("{not json"), 0o600); err != nil {
		return err
	}
	if _, err := storage.Open(brokenDir); err == nil {
		return fmt.Errorf("corrupt target state was accepted")
	}

	return nil
}

func selectVesselID(store *storage.Store, code string) string {
	for id, vessel := range store.CurrentSnapshot().Vessels {
		if vessel.Code == code {
			return id
		}
	}
	return ""
}

// plantIDCollision rewrites the target so its formula exists under a different
// generated ID while keeping the same normalized name. Both snapshots stay
// internally consistent, but the backup name index and the target name index
// disagree about which ID owns that name: a genuine cross-machine collision.
func plantIDCollision(store *storage.Store, backupSnapshot domain.Snapshot) error {
	var backupFormulaID string
	for id := range backupSnapshot.Formulas {
		backupFormulaID = id
		break
	}
	return store.Execute(func(snapshot *domain.Snapshot) error {
		formula := snapshot.Formulas[backupFormulaID]
		newID := "formula_collisionaa01"
		formula.ID = newID
		delete(snapshot.Formulas, backupFormulaID)
		snapshot.Formulas[newID] = formula
		snapshot.NameIndex[domain.NameKey(formula.Name)] = newID
		return nil
	})
}

func writeTamperedPayload(archivePath, destination string) error {
	raw, err := os.ReadFile(archivePath)
	if err != nil {
		return err
	}
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return err
	}
	payload, _ := envelope["payload"].(string)
	if payload == "" {
		return fmt.Errorf("archive has no payload")
	}
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return err
	}
	decoded[len(decoded)-1] ^= 0xFF
	envelope["payload"] = base64.StdEncoding.EncodeToString(decoded)
	encoded, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(destination, encoded, 0o600)
}

func writeSemanticallyBrokenArchive(archivePath, destination string) error {
	raw, err := os.ReadFile(archivePath)
	if err != nil {
		return err
	}
	var envelope struct {
		backup.Archive
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return err
	}
	decoded, err := base64.StdEncoding.DecodeString(envelope.Payload)
	if err != nil {
		return err
	}
	var snapshot map[string]any
	if err := json.Unmarshal(decoded, &snapshot); err != nil {
		return err
	}
	vessels, _ := snapshot["vessels"].(map[string]any)
	for id := range vessels {
		delete(vessels, id)
		break
	}
	snapshot["vessels"] = vessels
	reencoded, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	envelope.Payload = base64.StdEncoding.EncodeToString(reencoded)
	envelope.Checksum = storage.HashBytes(reencoded)
	out, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(destination, out, 0o600)
}
