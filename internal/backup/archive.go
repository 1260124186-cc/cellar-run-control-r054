// Package backup implements self-contained, checksummed cellar snapshots used
// to migrate the controller between machines. An archive embeds the complete
// canonical snapshot (formulas, vessels, runs with their observations, and the
// derived lookup indexes) together with a manifest and a SHA-256 checksum, so
// a restore can prove the file is complete before it changes anything.
package backup

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/storage"
)

const (
	// FormatName identifies cellar backup archives.
	FormatName = "cellar-run-control-backup"
	// FormatVersion is the only archive layout this build produces and reads.
	FormatVersion = 1
	// ChecksumSHA256 marks a hex SHA-256 digest over the payload bytes.
	ChecksumSHA256 = "sha256"
)

// Manifest counts every fact the payload must contain.
type Manifest struct {
	Formulas     int `json:"formulas"`
	Vessels      int `json:"vessels"`
	Runs         int `json:"runs"`
	Observations int `json:"observations"`
	Cleaning     int `json:"cleaning_facts"`
}

// Archive is the on-disk backup envelope.
type Archive struct {
	Format          string    `json:"format"`
	FormatVersion   int       `json:"format_version"`
	CreatedAt       time.Time `json:"created_at"`
	SourceRevision  int64     `json:"source_revision"`
	SourceUpdatedAt time.Time `json:"source_updated_at"`
	Manifest        Manifest  `json:"manifest"`
	// ChecksumAlgorithm is always ChecksumSHA256.
	ChecksumAlgorithm string `json:"checksum_algorithm"`
	// Checksum is the hex digest of the decoded Payload bytes.
	Checksum string `json:"checksum"`
	// Payload is base64-encoded canonical compact JSON of a domain.Snapshot.
	Payload string `json:"payload"`
}

// ErrChecksumMismatch means the payload no longer matches its checksum; the
// archive must not be restored.
var ErrChecksumMismatch = errors.New("backup checksum does not match the payload")

// PayloadBytes decodes the embedded payload.
func (a Archive) PayloadBytes() ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(a.Payload)
	if err != nil {
		return nil, fmt.Errorf("decode backup payload: %w", err)
	}
	return raw, nil
}

// VerifyChecksum proves the embedded payload matches the recorded digest.
func (a Archive) VerifyChecksum() error {
	if a.ChecksumAlgorithm != ChecksumSHA256 {
		return fmt.Errorf("unsupported backup checksum algorithm %q", a.ChecksumAlgorithm)
	}
	raw, err := a.PayloadBytes()
	if err != nil {
		return err
	}
	if storage.HashBytes(raw) != a.Checksum {
		return ErrChecksumMismatch
	}
	return nil
}

// PayloadSnapshot decodes and normalizes the embedded snapshot. The checksum
// is verified before any data is trusted.
func (a Archive) PayloadSnapshot() (domain.Snapshot, error) {
	if err := a.VerifyChecksum(); err != nil {
		return domain.Snapshot{}, err
	}
	raw, err := a.PayloadBytes()
	if err != nil {
		return domain.Snapshot{}, err
	}
	var snapshot domain.Snapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return domain.Snapshot{}, fmt.Errorf("decode backup payload snapshot: %w", err)
	}
	return storage.NormalizeSnapshot(snapshot)
}

// BuildManifest counts the facts a snapshot carries.
func BuildManifest(snapshot domain.Snapshot) Manifest {
	manifest := Manifest{
		Formulas: len(snapshot.Formulas),
		Vessels:  len(snapshot.Vessels),
		Runs:     len(snapshot.Runs),
	}
	for _, run := range snapshot.Runs {
		manifest.Observations += len(run.Observations)
	}
	for _, vessel := range snapshot.Vessels {
		if vessel.State == domain.VesselCleaning {
			manifest.Cleaning++
		}
	}
	return manifest
}

// NewArchive wraps a snapshot into a checksummed, self-describing envelope.
func NewArchive(snapshot domain.Snapshot, createdAt time.Time) (Archive, []byte, error) {
	payload, err := storage.CanonicalBytes(snapshot)
	if err != nil {
		return Archive{}, nil, err
	}
	archive := Archive{
		Format:            FormatName,
		FormatVersion:     FormatVersion,
		CreatedAt:         createdAt.UTC(),
		SourceRevision:    snapshot.Revision,
		SourceUpdatedAt:   snapshot.UpdatedAt.UTC(),
		Manifest:          BuildManifest(snapshot),
		ChecksumAlgorithm: ChecksumSHA256,
		Checksum:          storage.HashBytes(payload),
		Payload:           base64.StdEncoding.EncodeToString(payload),
	}
	encoded, err := json.MarshalIndent(archive, "", "  ")
	if err != nil {
		return Archive{}, nil, fmt.Errorf("encode backup archive: %w", err)
	}
	return archive, append(encoded, '\n'), nil
}

// OpenArchive reads an archive from disk and rejects anything that is not a
// well-formed, checksum-valid cellar backup.
func OpenArchive(path string) (Archive, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Archive{}, fmt.Errorf("read backup %s: %w", path, err)
	}
	return DecodeArchive(raw)
}

// DecodeArchive parses archive bytes without trusting them.
func DecodeArchive(raw []byte) (Archive, error) {
	var archive Archive
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&archive); err != nil {
		return Archive{}, fmt.Errorf("decode backup archive: %w", err)
	}
	if archive.Format != FormatName {
		return Archive{}, fmt.Errorf("not a cellar backup: format %q", archive.Format)
	}
	if archive.FormatVersion != FormatVersion {
		return Archive{}, fmt.Errorf("unsupported backup format version %d", archive.FormatVersion)
	}
	if archive.Checksum == "" || archive.Payload == "" {
		return Archive{}, errors.New("backup archive is missing its checksum or payload")
	}
	if err := archive.VerifyChecksum(); err != nil {
		return Archive{}, err
	}
	return archive, nil
}

// defaultBackupName mirrors the archive creation time in a portable filename.
func defaultBackupName(createdAt time.Time) string {
	return "cellar-backup-" + createdAt.UTC().Format("20060102T150405Z") + ".json"
}

func resolveOutputPath(output string, createdAt time.Time) string {
	if output == "" || output == "-" {
		return defaultBackupName(createdAt)
	}
	info, err := os.Stat(output)
	if err == nil && info.IsDir() {
		return filepath.Join(output, defaultBackupName(createdAt))
	}
	return output
}
