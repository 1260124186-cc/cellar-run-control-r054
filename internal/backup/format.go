// Package backup implements offline cellar snapshot backup and restore.
//
// A backup is a single self-describing JSON envelope. Its payload is the
// complete cellar snapshot (formulas, vessels, runs, observations) encoded
// with canonical (sorted-key) JSON. The manifest records format, revision,
// row counts, timestamps, and provenance. Both the payload and the full
// envelope carry SHA-256 checksums so a moved or copied backup can be
// verified independently before a restore.
package backup

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
)

const (
	// FormatVersion is the only backup envelope version accepted.
	FormatVersion = 1
	// FileExtension is the conventional suffix for backup files.
	FileExtension = ".cellar-backup.json"
	// AlgorithmSHA256 identifies the payload and envelope digest algorithm.
	AlgorithmSHA256 = "sha256"
)

// Counts records the rows contained in a backup payload.
type Counts struct {
	Formulas     int `json:"formulas"`
	Vessels      int `json:"vessels"`
	Runs         int `json:"runs"`
	Observations int `json:"observations"`
}

// Manifest is the human- and machine-readable backup summary.
type Manifest struct {
	FormatVersion int      `json:"format_version"`
	Algorithm     string   `json:"algorithm"`
	Revision      int64    `json:"snapshot_revision"`
	Counts        Counts   `json:"counts"`
	Source        string   `json:"source"`
	CreatedAt     string   `json:"created_at"`
	SnapshotAt    string   `json:"snapshot_updated_at"`
	PayloadBytes  int      `json:"payload_bytes"`
	Note          string   `json:"note,omitempty"`
	SignedFields  []string `json:"signed_fields"`
}

// Signature authenticates the manifest content.
type Signature struct {
	Algorithm   string `json:"algorithm"`
	SignedBytes string `json:"signed"`
	Digest      string `json:"sha256"`
}

// Envelope is the on-disk backup document.
type Envelope struct {
	Manifest       Manifest        `json:"manifest"`
	Payload        json.RawMessage `json:"payload"`
	PayloadSHA256  string          `json:"payload_sha256"`
	Signature      Signature       `json:"signature"`
	EnvelopeSHA256 string          `json:"envelope_sha256"`
}

// MarshalJSON keeps the canonical compact payload verbatim inside an
// otherwise indented envelope. Re-indenting the RawMessage would change its
// bytes and invalidate the payload checksum.
func (e Envelope) MarshalJSON() ([]byte, error) {
	payload, err := canonicalEncode(e.Payload)
	if err != nil {
		return nil, err
	}
	manifest, err := json.MarshalIndent(e.Manifest, "  ", "  ")
	if err != nil {
		return nil, err
	}
	signature, err := json.MarshalIndent(e.Signature, "  ", "  ")
	if err != nil {
		return nil, err
	}
	buffer := &bytes.Buffer{}
	fmt.Fprintf(buffer, "{\n  \"manifest\": %s,\n", indentBlock(manifest, "  "))
	fmt.Fprintf(buffer, "  \"payload\": %s,\n", payload)
	fmt.Fprintf(buffer, "  \"payload_sha256\": %q,\n", e.PayloadSHA256)
	fmt.Fprintf(buffer, "  \"signature\": %s,\n", indentBlock(signature, "  "))
	fmt.Fprintf(buffer, "  \"envelope_sha256\": %q\n}", e.EnvelopeSHA256)
	return buffer.Bytes(), nil
}

// indentBlock prefixes every line after the first with padding so the value
// aligns under its JSON key.
func indentBlock(raw []byte, padding string) []byte {
	return bytes.ReplaceAll(raw, []byte("\n"), []byte("\n"+padding))
}

func canonicalEncode(value any) ([]byte, error) {
	buffer := &bytes.Buffer{}
	encoder := json.NewEncoder(buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buffer.Bytes(), "\n"), nil
}

// EncodePayload renders a snapshot as canonical compact JSON with every
// timestamp expressed in UTC, so the same cellar state always produces the
// same bytes and therefore the same checksum.
func EncodePayload(snapshot domain.Snapshot) (json.RawMessage, error) {
	normalized := snapshot.Clone()
	normalized.UpdatedAt = normalized.UpdatedAt.UTC()
	for id, formula := range normalized.Formulas {
		formula.CreatedAt = formula.CreatedAt.UTC()
		formula.UpdatedAt = formula.UpdatedAt.UTC()
		if formula.ApprovedAt != nil {
			stamp := formula.ApprovedAt.UTC()
			formula.ApprovedAt = &stamp
		}
		if formula.RetiredAt != nil {
			stamp := formula.RetiredAt.UTC()
			formula.RetiredAt = &stamp
		}
		normalized.Formulas[id] = formula
	}
	for id, vessel := range normalized.Vessels {
		vessel.CreatedAt = vessel.CreatedAt.UTC()
		vessel.UpdatedAt = vessel.UpdatedAt.UTC()
		normalized.Vessels[id] = vessel
	}
	for id, run := range normalized.Runs {
		run.CreatedAt = run.CreatedAt.UTC()
		run.UpdatedAt = run.UpdatedAt.UTC()
		if run.StartedAt != nil {
			stamp := run.StartedAt.UTC()
			run.StartedAt = &stamp
		}
		if run.EndedAt != nil {
			stamp := run.EndedAt.UTC()
			run.EndedAt = &stamp
		}
		for index := range run.Observations {
			run.Observations[index].ObservedAt = run.Observations[index].ObservedAt.UTC()
			run.Observations[index].ReceivedAt = run.Observations[index].ReceivedAt.UTC()
		}
		normalized.Runs[id] = run
	}
	raw, err := canonicalEncode(normalized)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(raw), nil
}

// DecodePayload parses a canonical payload into a snapshot without applying
// normalization; callers validate it through storage.NormalizeSnapshot.
func DecodePayload(raw json.RawMessage) (domain.Snapshot, error) {
	var snapshot domain.Snapshot

	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&snapshot); err != nil {
		return domain.Snapshot{}, err
	}
	return snapshot, nil
}

func digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func stamp(value time.Time) string {
	if value.IsZero() {
		// A never-written snapshot carries no snapshot time; an empty string
		// stays deterministic across repeated backups of an empty cellar.
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
