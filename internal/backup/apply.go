package backup

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/storage"
)

// DefaultSafetyCopyName is kept as documentation of the rollback file family;
// actual defaults are timestamped so repeated restores never overwrite an
// earlier rollback point.
const DefaultSafetyCopyName = "state.pre-restore.json"

// ApplyOptions control an apply.
type ApplyOptions struct {
	// ExpectedTargetHash must match the target as it was when the plan was
	// shown to the operator. A mismatch aborts before any file changes.
	ExpectedTargetHash string
	// SafetyCopyPath overrides where the pre-restore snapshot is kept.
	SafetyCopyPath string
	// OverwriteSafetyCopy permits replacing an existing safety copy.
	OverwriteSafetyCopy bool
}

// ApplyResult reports the completed switch.
type ApplyResult struct {
	InstalledHash     string
	Revision          int64
	SafetyCopyCreated bool
	SafetyCopyPath    string
}

// Apply switches the store to the plan's snapshot. It reverifies the backup
// content, rechecks that the target is still the snapshot the plan described,
// preserves the current state in a safety copy, and hands the atomic switch
// (with post-switch verification and automatic rollback) to the storage layer.
func Apply(store *storage.Store, plan Plan, options ApplyOptions) (ApplyResult, error) {
	if err := VerifySnapshot(plan.Backup, plan.Manifest); err != nil {
		return ApplyResult{}, err
	}
	if options.ExpectedTargetHash != "" && options.ExpectedTargetHash != plan.TargetHash {
		return ApplyResult{}, errors.New("the restore plan was built with a different target snapshot")
	}
	safetyPath := options.SafetyCopyPath
	if safetyPath == "" {
		safetyPath = uniqueSafetyCopyPath(filepath.Dir(store.Path()))
	}
	result, err := store.Replace(plan.Backup, storage.ReplaceOptions{
		ExpectedHash:        plan.TargetHash,
		SafetyCopyPath:      safetyPath,
		OverwriteSafetyCopy: options.OverwriteSafetyCopy,
	})
	if err != nil {
		return ApplyResult{}, err
	}
	return ApplyResult{
		InstalledHash:     result.InstalledHash,
		Revision:          result.Revision,
		SafetyCopyCreated: result.SafetyCopyCreated,
		SafetyCopyPath:    result.SafetyCopyPath,
	}, nil
}

// VerifySnapshot runs the same content checks used for an on-disk archive
// against an in-memory snapshot and manifest.
func VerifySnapshot(snapshot domain.Snapshot, manifest Manifest) error {
	normalized, err := storage.NormalizeSnapshot(snapshot)
	if err != nil {
		return err
	}
	problems := validateContent(normalized)
	problems = append(problems, verifyManifest(manifest, BuildManifest(normalized))...)
	if len(problems) == 0 {
		return nil
	}
	return errors.New("backup failed verification and was not restored:\n  - " +
		strings.Join(problems, "\n  - "))
}

// uniqueSafetyCopyPath returns a timestamped rollback path in directory that
// does not yet exist, adding a numeric suffix when multiple restores happen in
// the same second.
func uniqueSafetyCopyPath(directory string) string {
	stem := "state.pre-restore-" + time.Now().UTC().Format("20060102T150405Z")
	candidate := filepath.Join(directory, stem+".json")
	if _, err := os.Stat(candidate); errors.Is(err, os.ErrNotExist) {
		return candidate
	}
	for suffix := 2; ; suffix++ {
		next := filepath.Join(directory, stem+"-"+strconv.Itoa(suffix)+".json")
		if _, err := os.Stat(next); errors.Is(err, os.ErrNotExist) {
			return next
		}
	}
}
