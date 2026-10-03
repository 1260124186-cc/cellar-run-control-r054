package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
)

// CanonicalSnapshot returns a copy with all slices and indexes represented in
// a single stable shape so the same logical data always serializes to the same
// bytes, regardless of whether a slice was decoded as nil or empty.
func CanonicalSnapshot(value domain.Snapshot) domain.Snapshot {
	canonical, _ := NormalizeSnapshot(value)
	for id, formula := range canonical.Formulas {
		if formula.Steps == nil {
			formula.Steps = []domain.FormulaStep{}
		}
		canonical.Formulas[id] = formula
	}
	for id, run := range canonical.Runs {
		if run.Observations == nil {
			run.Observations = []domain.GravityObservation{}
		}
		canonical.Runs[id] = run
	}
	return canonical
}

// CanonicalBytes encodes a snapshot in the canonical compact JSON form used
// inside backup archives.
func CanonicalBytes(value domain.Snapshot) ([]byte, error) {
	encoded, err := json.Marshal(CanonicalSnapshot(value))
	if err != nil {
		return nil, fmt.Errorf("encode canonical snapshot: %w", err)
	}
	return encoded, nil
}

// SnapshotHash returns the lowercase hex SHA-256 digest of a snapshot's
// canonical encoding.
func SnapshotHash(value domain.Snapshot) (string, error) {
	encoded, err := CanonicalBytes(value)
	if err != nil {
		return "", err
	}
	return HashBytes(encoded), nil
}

// HashBytes returns the lowercase hex SHA-256 digest of raw bytes.
func HashBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
