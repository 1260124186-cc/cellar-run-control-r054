package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
)

const stateFileName = "state.json"

type Store struct {
	mu       sync.RWMutex
	path     string
	snapshot domain.Snapshot
	now      func() time.Time
}

func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	store := &Store{
		path:     filepath.Join(dataDir, stateFileName),
		snapshot: domain.EmptySnapshot(),
		now:      func() time.Time { return time.Now().UTC() },
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) load() error {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read state: %w", err)
	}
	var loaded domain.Snapshot
	if err := json.Unmarshal(raw, &loaded); err != nil {
		return fmt.Errorf("decode state: %w", err)
	}
	normalized, err := NormalizeSnapshot(loaded)
	if err != nil {
		return err
	}
	s.snapshot = normalized
	return nil
}

// NormalizeSnapshot validates a decoded snapshot and rebuilds no derived data:
// when an index does not exactly match its collection the snapshot is rejected
// rather than silently rewritten.
func NormalizeSnapshot(value domain.Snapshot) (domain.Snapshot, error) {
	if value.Revision < 0 {
		return domain.Snapshot{}, fmt.Errorf("state revision cannot be negative")
	}
	if value.Formulas == nil {
		value.Formulas = map[string]domain.Formula{}
	}
	if value.Vessels == nil {
		value.Vessels = map[string]domain.Vessel{}
	}
	if value.Runs == nil {
		value.Runs = map[string]domain.FermentationRun{}
	}
	if value.NameIndex == nil {
		value.NameIndex = map[string]string{}
	}
	if value.VesselIndex == nil {
		value.VesselIndex = map[string]string{}
	}
	if value.RunIndex == nil {
		value.RunIndex = map[string]string{}
	}
	if err := validateIndexes(value); err != nil {
		return domain.Snapshot{}, err
	}
	return value, nil
}

func validateIndexes(value domain.Snapshot) error {
	expectedNames := make(map[string]string, len(value.Formulas))
	for id, formula := range value.Formulas {
		key := domain.NameKey(formula.Name)
		if key == "" {
			return fmt.Errorf("formula %s has an empty normalized name", id)
		}
		if existing, found := expectedNames[key]; found && existing != id {
			return fmt.Errorf("formula name index contains a duplicate key")
		}
		expectedNames[key] = id
	}
	if err := compareIndexes("formula name", value.NameIndex, expectedNames); err != nil {
		return err
	}

	expectedVessels := make(map[string]string, len(value.Vessels))
	for id, vessel := range value.Vessels {
		if existing, found := expectedVessels[vessel.Code]; found && existing != id {
			return fmt.Errorf("vessel index contains a duplicate code")
		}
		expectedVessels[vessel.Code] = id
	}
	if err := compareIndexes("vessel code", value.VesselIndex, expectedVessels); err != nil {
		return err
	}

	expectedRuns := make(map[string]string, len(value.Runs))
	for id, run := range value.Runs {
		if existing, found := expectedRuns[run.Code]; found && existing != id {
			return fmt.Errorf("run index contains a duplicate code")
		}
		expectedRuns[run.Code] = id
	}
	return compareIndexes("run code", value.RunIndex, expectedRuns)
}

func compareIndexes(label string, actual, expected map[string]string) error {
	if len(actual) != len(expected) {
		return fmt.Errorf("%s index does not match its collection", label)
	}
	for key, expectedID := range expected {
		actualID, found := actual[key]
		if !found || actualID != expectedID {
			return fmt.Errorf("%s index does not match its collection", label)
		}
	}
	return nil
}

func (s *Store) Revision() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshot.Revision
}

func (s *Store) View(visitor func(domain.Snapshot) error) error {
	s.mu.RLock()
	copied := s.snapshot.Clone()
	s.mu.RUnlock()
	return visitor(copied)
}

func (s *Store) Update(expected *int64, mutate func(*domain.Snapshot) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if expected != nil && *expected != s.snapshot.Revision {
		return domain.NewError(domain.CodeRevisionConflict,
			"the supplied revision does not match the current snapshot")
	}
	candidate := s.snapshot.Clone()
	if err := mutate(&candidate); err != nil {
		return err
	}
	candidate.Revision = s.snapshot.Revision + 1
	candidate.UpdatedAt = s.now()
	if err := writeSnapshot(s.path, candidate); err != nil {
		return domain.NewError(domain.CodeStorage, err.Error())
	}
	s.snapshot = candidate
	return nil
}

func (s *Store) Execute(mutator func(*domain.Snapshot) error) error {
	return s.Update(nil, mutator)
}

// Path reports the snapshot file backing the store.
func (s *Store) Path() string {
	return s.path
}

// CurrentSnapshot returns a defensive copy of the in-memory snapshot.
func (s *Store) CurrentSnapshot() domain.Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshot.Clone()
}

// ErrSnapshotChanged indicates the on-disk state changed between planning a
// restore and applying it.
var ErrSnapshotChanged = errors.New("target snapshot changed between the plan and the restore")

// ReplaceOptions control an offline snapshot replacement.
type ReplaceOptions struct {
	// ExpectedHash, when set, must match the current snapshot's canonical hash.
	ExpectedHash string
	// SafetyCopyPath receives the pre-restore snapshot whenever one exists.
	// The file is written atomically and is kept after the operation so an
	// operator can roll back manually if the restored state proves wrong.
	SafetyCopyPath string
	// OverwriteSafetyCopy allows replacing an existing safety copy. Without it,
	// an earlier rollback point is never silently destroyed.
	OverwriteSafetyCopy bool
}

// ReplaceResult reports what a replacement did.
type ReplaceResult struct {
	InstalledHash string
	Revision      int64
	// SafetyCopyCreated is true when the previous state was preserved.
	SafetyCopyCreated bool
	// SafetyCopyPath is the location of the preserved state.
	SafetyCopyPath string
}

// Replace installs candidate as the new snapshot. It serializes all writers
// and readers, preserves the current state first, installs the replacement
// atomically, and re-reads the installed bytes to prove the switch succeeded.
// Any failure before the rename leaves the live snapshot untouched; a failure
// after the rename triggers an automatic rollback to the preserved state.
func (s *Store) Replace(candidate domain.Snapshot, options ReplaceOptions) (ReplaceResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	normalized, err := NormalizeSnapshot(candidate)
	if err != nil {
		return ReplaceResult{}, fmt.Errorf("replacement snapshot is invalid: %w", err)
	}
	desiredHash, err := SnapshotHash(normalized)
	if err != nil {
		return ReplaceResult{}, err
	}
	currentHash, err := SnapshotHash(s.snapshot)
	if err != nil {
		return ReplaceResult{}, err
	}
	if options.ExpectedHash != "" && options.ExpectedHash != currentHash {
		return ReplaceResult{}, ErrSnapshotChanged
	}

	safetyCreated := false
	if _, statErr := os.Stat(s.path); statErr == nil {
		if options.SafetyCopyPath == "" {
			return ReplaceResult{}, errors.New("a safety copy path is required when state exists")
		}
		if _, copyExists := os.Stat(options.SafetyCopyPath); copyExists == nil &&
			!options.OverwriteSafetyCopy {
			return ReplaceResult{}, fmt.Errorf(
				"safety copy %s already exists; remove it, choose another path, or allow its replacement",
				options.SafetyCopyPath)
		}
		raw, readErr := os.ReadFile(s.path)
		if readErr != nil {
			return ReplaceResult{}, fmt.Errorf("read current state for safety copy: %w", readErr)
		}
		if copyErr := AtomicWriteFile(options.SafetyCopyPath, 0o600, func(handle *os.File) error {
			if _, writeErr := handle.Write(raw); writeErr != nil {
				return fmt.Errorf("write safety copy: %w", writeErr)
			}
			return nil
		}); copyErr != nil {
			return ReplaceResult{}, copyErr
		}
		safetyCreated = true
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return ReplaceResult{}, fmt.Errorf("inspect current state: %w", statErr)
	}

	if err := writeSnapshot(s.path, normalized); err != nil {
		return ReplaceResult{}, domain.NewError(domain.CodeStorage, err.Error())
	}

	installed, verifyErr := readSnapshotFile(s.path)
	if verifyErr != nil {
		rollbackErr := s.rollbackAfterReplace(options.SafetyCopyPath, safetyCreated)
		if rollbackErr != nil {
			return ReplaceResult{}, fmt.Errorf("verify replaced state: %v (rollback also failed: %v)",
				verifyErr, rollbackErr)
		}
		return ReplaceResult{}, fmt.Errorf("verify replaced state: %w (rolled back to previous state)", verifyErr)
	}
	installedHash, hashErr := SnapshotHash(installed)
	if hashErr != nil {
		rollbackErr := s.rollbackAfterReplace(options.SafetyCopyPath, safetyCreated)
		if rollbackErr != nil {
			return ReplaceResult{}, fmt.Errorf("hash replaced state: %v (rollback also failed: %v)",
				hashErr, rollbackErr)
		}
		return ReplaceResult{}, fmt.Errorf("hash replaced state: %w (rolled back to previous state)", hashErr)
	}
	if installedHash != desiredHash {
		rollbackErr := s.rollbackAfterReplace(options.SafetyCopyPath, safetyCreated)
		if rollbackErr != nil {
			return ReplaceResult{}, fmt.Errorf("installed state differs from the verified backup (rollback also failed: %v)",
				rollbackErr)
		}
		return ReplaceResult{}, errors.New("installed state differs from the verified backup (rolled back to previous state)")
	}

	s.snapshot = installed
	return ReplaceResult{
		InstalledHash:     installedHash,
		Revision:          installed.Revision,
		SafetyCopyCreated: safetyCreated,
		SafetyCopyPath:    options.SafetyCopyPath,
	}, nil
}

func (s *Store) rollbackAfterReplace(safetyPath string, safetyCreated bool) error {
	if !safetyCreated {
		if removeErr := os.Remove(s.path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return fmt.Errorf("remove unverified state: %w", removeErr)
		}
		s.snapshot = domain.EmptySnapshot()
		return nil
	}
	raw, err := os.ReadFile(safetyPath)
	if err != nil {
		return fmt.Errorf("read safety copy: %w", err)
	}
	var restored domain.Snapshot
	if err := json.Unmarshal(raw, &restored); err != nil {
		return fmt.Errorf("decode safety copy: %w", err)
	}
	normalized, err := NormalizeSnapshot(restored)
	if err != nil {
		return fmt.Errorf("safety copy is invalid: %w", err)
	}
	if err := writeSnapshot(s.path, normalized); err != nil {
		return err
	}
	s.snapshot = normalized
	return nil
}

func readSnapshotFile(path string) (domain.Snapshot, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return domain.Snapshot{}, fmt.Errorf("read state: %w", err)
	}
	var loaded domain.Snapshot
	if err := json.Unmarshal(raw, &loaded); err != nil {
		return domain.Snapshot{}, fmt.Errorf("decode state: %w", err)
	}
	return NormalizeSnapshot(loaded)
}
