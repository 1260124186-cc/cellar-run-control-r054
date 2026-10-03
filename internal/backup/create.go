package backup

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/storage"
)

// CreateResult reports the outcome of a backup attempt.
type CreateResult struct {
	Path      string
	Skipped   bool
	Overwrote bool
	Envelope  Envelope
	Inventory Inventory
	Bytes     int
}

// CreateOptions controls backup creation.
type CreateOptions struct {
	// OutputPath is the destination file. When empty a deterministic name in
	// OutputDir is used.
	OutputPath string
	OutputDir  string
	// Source labels the producing machine in the manifest.
	Source string
	Note   string
	// Overwrite allows replacing an existing different backup file.
	Overwrite bool
}

// Create validates the snapshot, lists the inventory, and writes a checksummed
// backup envelope. If an identical envelope already exists at the destination
// it is re-verified and the result reports Skipped instead of rewriting it.
func Create(snapshot domain.Snapshot, options CreateOptions) (CreateResult, error) {
	normalized, err := storage.NormalizeSnapshot(snapshot.Clone())
	if err != nil {
		return CreateResult{}, fmt.Errorf("snapshot indexes are invalid: %w", err)
	}
	if err := ValidateSnapshot(normalized); err != nil {
		return CreateResult{}, err
	}
	inventory := BuildInventory(normalized)

	// Envelope content is deterministic in the cellar state: the manifest
	// time is the snapshot update time, so backing up the same state twice
	// yields the same bytes and the repeated run is a verifiable no-op.
	envelope, raw, err := buildEnvelope(normalized, inventory.Counts, options)
	if err != nil {
		return CreateResult{}, err
	}

	path := options.OutputPath
	if path == "" {
		directory := options.OutputDir
		if directory == "" {
			directory = "."
		}
		path = filepath.Join(directory, DefaultFileName(normalized))
	}

	result := CreateResult{Path: path, Envelope: envelope, Inventory: inventory, Bytes: len(raw)}

	if existing, err := os.ReadFile(path); err == nil {
		if bytes.Equal(existing, raw) {
			if _, verifyErr := VerifyEnvelope(existing); verifyErr != nil {
				return CreateResult{}, fmt.Errorf("destination %s already exists with the same bytes but fails verification: %w", path, verifyErr)
			}
			result.Skipped = true
			return result, nil
		}
		if !options.Overwrite {
			return CreateResult{}, fmt.Errorf(
				"backup destination %s already exists with different content; rerun with --force to replace it",
				path)
		}
		result.Overwrote = true
	} else if !os.IsNotExist(err) {
		return CreateResult{}, fmt.Errorf("inspect backup destination: %w", err)
	}

	if err := WriteFileAtomic(path, raw, options.Overwrite || result.Overwrote); err != nil {
		return CreateResult{}, err
	}
	return result, nil
}

func buildEnvelope(
	snapshot domain.Snapshot,
	counts Counts,
	options CreateOptions,
) (Envelope, []byte, error) {
	payload, err := EncodePayload(snapshot)
	if err != nil {
		return Envelope{}, nil, fmt.Errorf("encode backup payload: %w", err)
	}
	// The manifest is deterministic in the cellar state (its time is the
	// snapshot update time), so backing up the same state twice yields the
	// same bytes and a repeated run is a verifiable no-op.
	envelope := Envelope{
		Manifest: Manifest{
			FormatVersion: FormatVersion,
			Algorithm:     AlgorithmSHA256,
			Revision:      snapshot.Revision,
			Counts:        counts,
			Source:        options.Source,
			CreatedAt:     stamp(snapshot.UpdatedAt),
			SnapshotAt:    stamp(snapshot.UpdatedAt),
			PayloadBytes:  len(payload),
			Note:          options.Note,
			SignedFields:  []string{"manifest"},
		},
		Payload:       payload,
		PayloadSHA256: digest(payload),
	}

	// The signature authenticates the manifest content; the envelope digest
	// then authenticates the complete document including the signature, with
	// only the envelope digest field itself blanked during its own check.
	manifestBytes, err := canonicalEncode(envelope.Manifest)
	if err != nil {
		return Envelope{}, nil, fmt.Errorf("encode backup manifest for signing: %w", err)
	}
	envelope.Signature = Signature{
		Algorithm:   AlgorithmSHA256,
		SignedBytes: "manifest",
		Digest:      digest(manifestBytes),
	}

	signedBytes, err := canonicalEncode(envelopeWithoutDigest(envelope))
	if err != nil {
		return Envelope{}, nil, fmt.Errorf("encode backup envelope for sealing: %w", err)
	}
	envelope.EnvelopeSHA256 = digest(signedBytes)

	raw, err := envelope.MarshalJSON()
	if err != nil {
		return Envelope{}, nil, fmt.Errorf("encode backup envelope: %w", err)
	}
	raw = append(raw, '\n')
	return envelope, raw, nil
}

func envelopeWithoutDigest(envelope Envelope) Envelope {
	clone := envelope
	clone.EnvelopeSHA256 = ""
	return clone
}

// WriteFileAtomic writes bytes to path through a durable same-directory temp
// file and rename, so an interrupted write never leaves a partial backup.
func WriteFileAtomic(path string, raw []byte, allowReplace bool) error {
	directory := filepath.Dir(path)
	if directory == "" {
		directory = "."
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("create backup directory: %w", err)
	}
	handle, err := os.CreateTemp(directory, ".cellar-backup-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary backup: %w", err)
	}
	tempName := handle.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tempName)
		}
	}()
	if _, err := handle.Write(raw); err != nil {
		_ = handle.Close()
		return fmt.Errorf("write backup bytes: %w", err)
	}
	if err := handle.Sync(); err != nil {
		_ = handle.Close()
		return fmt.Errorf("commit backup bytes: %w", err)
	}
	if err := handle.Close(); err != nil {
		return fmt.Errorf("close backup file: %w", err)
	}
	if err := os.Rename(tempName, path); err != nil {
		return fmt.Errorf("place backup file: %w", err)
	}
	cleanup = false
	if dir, err := os.Open(directory); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}
