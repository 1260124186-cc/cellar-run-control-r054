package backup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/storage"
)

// IntegrityError aggregates every structural problem found in a backup so an
// operator sees the full repair surface instead of the first failure.
type IntegrityError struct {
	Problems []string
}

func (e *IntegrityError) Error() string {
	if len(e.Problems) == 1 {
		return "backup integrity: " + e.Problems[0]
	}
	return fmt.Sprintf("backup integrity: %d problems, first: %s",
		len(e.Problems), e.Problems[0])
}

func integrityError(problems []string) error {
	if len(problems) == 0 {
		return nil
	}
	return &IntegrityError{Problems: problems}
}

// Report describes a successfully verified backup.
type Report struct {
	Envelope Envelope
	Snapshot domain.Snapshot
	Counts   Counts
}

// VerifyEnvelope parses, authenticates, decodes, and structurally validates a
// backup envelope. It checks both the payload digest and the signed envelope
// digest before trusting any contained data.
func VerifyEnvelope(raw []byte) (Report, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var envelope Envelope
	if err := decoder.Decode(&envelope); err != nil {
		return Report{}, fmt.Errorf("parse backup envelope: %w", err)
	}

	problems := make([]string, 0)
	if envelope.Manifest.FormatVersion != FormatVersion {
		problems = append(problems, fmt.Sprintf(
			"unsupported backup format_version %d (this tool supports %d)",
			envelope.Manifest.FormatVersion, FormatVersion))
	}
	if envelope.Manifest.Algorithm != AlgorithmSHA256 ||
		envelope.PayloadSHA256 == "" ||
		envelope.Signature.Algorithm != AlgorithmSHA256 ||
		envelope.EnvelopeSHA256 == "" {
		problems = append(problems, "backup must declare sha256 payload and envelope checksums")
	}
	if len(envelope.Payload) == 0 {
		problems = append(problems, "backup payload is empty")
		return Report{}, integrityError(problems)
	}

	payloadDigest := digest(envelope.Payload)
	if payloadDigest != envelope.PayloadSHA256 {
		problems = append(problems, fmt.Sprintf(
			"payload checksum mismatch: envelope claims %s, payload hashes to %s",
			envelope.PayloadSHA256, payloadDigest))
	}

	signedCopy := envelope
	signedCopy.EnvelopeSHA256 = ""
	signedBytes, err := canonicalEncode(signedCopy)
	if err != nil {
		return Report{}, fmt.Errorf("re-encode envelope for verification: %w", err)
	}
	envelopeDigest := digest(signedBytes)
	if envelopeDigest != envelope.EnvelopeSHA256 {
		problems = append(problems, fmt.Sprintf(
			"envelope checksum mismatch: recorded %s, recomputed %s",
			envelope.EnvelopeSHA256, envelopeDigest))
	}
	manifestBytes, err := canonicalEncode(envelope.Manifest)
	if err != nil {
		return Report{}, fmt.Errorf("re-encode manifest for verification: %w", err)
	}
	if envelope.Signature.Digest != digest(manifestBytes) {
		problems = append(problems, fmt.Sprintf(
			"manifest signature mismatch: recorded %s, recomputed %s",
			envelope.Signature.Digest, digest(manifestBytes)))
	}
	if err := integrityError(problems); err != nil {
		return Report{}, err
	}

	snapshot, err := DecodePayload(envelope.Payload)
	if err != nil {
		return Report{}, fmt.Errorf("decode backup payload: %w", err)
	}
	if normalized, err := storage.NormalizeSnapshot(snapshot); err != nil {
		return Report{}, fmt.Errorf("backup payload indexes are invalid: %w", err)
	} else {
		snapshot = normalized
	}
	if err := ValidateSnapshot(snapshot); err != nil {
		return Report{}, err
	}

	counts := CountSnapshot(snapshot)
	if counts != envelope.Manifest.Counts {
		return Report{}, integrityError([]string{fmt.Sprintf(
			"manifest counts %+v do not match payload counts %+v",
			envelope.Manifest.Counts, counts)})
	}
	if envelope.Manifest.PayloadBytes != len(envelope.Payload) {
		return Report{}, integrityError([]string{fmt.Sprintf(
			"manifest payload_bytes %d does not match encoded payload length %d",
			envelope.Manifest.PayloadBytes, len(envelope.Payload))})
	}
	if envelope.Manifest.Revision != snapshot.Revision {
		return Report{}, integrityError([]string{fmt.Sprintf(
			"manifest revision %d does not match payload revision %d",
			envelope.Manifest.Revision, snapshot.Revision)})
	}
	return Report{Envelope: envelope, Snapshot: snapshot, Counts: counts}, nil
}

// ValidateSnapshot checks referential and lifecycle consistency beyond the
// index checks performed by storage.NormalizeSnapshot.
func ValidateSnapshot(snapshot domain.Snapshot) error {
	problems := make([]string, 0)
	if snapshot.Revision < 0 {
		problems = append(problems, "snapshot revision cannot be negative")
	}

	for runID, run := range snapshot.Runs {
		if run.ID != runID {
			problems = append(problems, fmt.Sprintf("run map key %s does not match run id %s", runID, run.ID))
		}
		if _, ok := snapshot.Formulas[run.FormulaID]; !ok {
			problems = append(problems, fmt.Sprintf("run %s references missing formula %s", run.Code, run.FormulaID))
		} else if formula := snapshot.Formulas[run.FormulaID]; formula.State == domain.FormulaDraft {
			problems = append(problems, fmt.Sprintf("run %s references draft formula %s", run.Code, formula.Name))
		}
		if run.VesselID != nil {
			vessel, ok := snapshot.Vessels[*run.VesselID]
			if !ok {
				problems = append(problems, fmt.Sprintf("run %s references missing vessel %s", run.Code, *run.VesselID))
			} else if vessel.ActiveRunID == nil || *vessel.ActiveRunID != runID {
				problems = append(problems, fmt.Sprintf(
					"run %s points at vessel %s but that vessel is not assigned to the run",
					run.Code, vessel.Code))
			}
		}
		validateRunObservations(run, &problems)
		if run.Metrics != nil && run.Metrics.ObservationCount != len(run.Observations) {
			problems = append(problems, fmt.Sprintf(
				"run %s metrics report %d observations but the run contains %d",
				run.Code, run.Metrics.ObservationCount, len(run.Observations)))
		}
	}

	for vesselID, vessel := range snapshot.Vessels {
		if vessel.ID != vesselID {
			problems = append(problems, fmt.Sprintf("vessel map key %s does not match vessel id %s", vesselID, vessel.ID))
		}
		if vessel.ActiveRunID != nil {
			run, ok := snapshot.Runs[*vessel.ActiveRunID]
			if !ok {
				problems = append(problems, fmt.Sprintf("vessel %s references missing run %s", vessel.Code, *vessel.ActiveRunID))
			} else if run.VesselID == nil || *run.VesselID != vesselID {
				problems = append(problems, fmt.Sprintf(
					"vessel %s claims run %s but that run is not assigned to the vessel",
					vessel.Code, run.Code))
			}
		}
		switch vessel.State {
		case domain.VesselReserved, domain.VesselInUse, domain.VesselCleaning:
			if vessel.ActiveRunID == nil {
				problems = append(problems, fmt.Sprintf(
					"vessel %s is %s without an associated run", vessel.Code, vessel.State))
			}
		case domain.VesselAvailable, domain.VesselRetired:
			if vessel.ActiveRunID != nil {
				problems = append(problems, fmt.Sprintf(
					"vessel %s is %s but still carries an active run", vessel.Code, vessel.State))
			}
		default:
			problems = append(problems, fmt.Sprintf("vessel %s has unknown state %q", vessel.Code, vessel.State))
		}
	}

	for formulaID, formula := range snapshot.Formulas {
		if formula.ID != formulaID {
			problems = append(problems, fmt.Sprintf("formula map key %s does not match formula id %s", formulaID, formula.ID))
		}
		if domain.NameKey(formula.Name) == "" {
			problems = append(problems, fmt.Sprintf("formula %s has an empty name", formulaID))
		}
		if (formula.State == domain.FormulaApproved || formula.State == domain.FormulaRetired) &&
			len(formula.Steps) == 0 {
			problems = append(problems, fmt.Sprintf(
				"formula %s is %s without process steps", formula.Name, formula.State))
		}
		if formula.State != domain.FormulaDraft && formula.State != domain.FormulaApproved &&
			formula.State != domain.FormulaRetired {
			problems = append(problems, fmt.Sprintf("formula %s has unknown state %q", formula.Name, formula.State))
		}
	}
	return integrityError(problems)
}

func validateRunObservations(run domain.FermentationRun, problems *[]string) {
	if len(run.Observations) > 500 {
		*problems = append(*problems, fmt.Sprintf(
			"run %s contains %d observations (limit 500)", run.Code, len(run.Observations)))
	}
	var previous *domain.GravityObservation
	for index := range run.Observations {
		observation := run.Observations[index]
		if observation.RunID != "" && observation.RunID != run.ID {
			*problems = append(*problems, fmt.Sprintf(
				"run %s observation %d belongs to run %s",
				run.Code, observation.Sequence, observation.RunID))
		}
		if observation.Sequence != index+1 {
			*problems = append(*problems, fmt.Sprintf(
				"run %s observation sequences must be contiguous and begin at 1", run.Code))
		}
		if observation.Gravity < 0.980 || observation.Gravity > 1.200 {
			*problems = append(*problems, fmt.Sprintf(
				"run %s observation %d gravity %.3f is out of range",
				run.Code, observation.Sequence, observation.Gravity))
		}
		if observation.TemperatureC < -5 || observation.TemperatureC > 60 {
			*problems = append(*problems, fmt.Sprintf(
				"run %s observation %d temperature %.1f is out of range",
				run.Code, observation.Sequence, observation.TemperatureC))
		}
		if run.StartedAt != nil && observation.ObservedAt.Before(run.StartedAt.UTC()) {
			*problems = append(*problems, fmt.Sprintf(
				"run %s observation %d precedes the run start", run.Code, observation.Sequence))
		}
		if previous != nil {
			if !observation.ObservedAt.After(previous.ObservedAt) {
				*problems = append(*problems, fmt.Sprintf(
					"run %s observation times must strictly increase (at sequence %d)",
					run.Code, observation.Sequence))
			}
			if observation.Gravity-previous.Gravity > 0.010 {
				*problems = append(*problems, fmt.Sprintf(
					"run %s observation %d gravity rises more than 0.010",
					run.Code, observation.Sequence))
			}
		}
		previous = &run.Observations[index]
	}
}

// ErrFileChanged is returned when a backup file on disk no longer matches the
// bytes that were verified.
var ErrFileChanged = errors.New("backup file changed after verification")

// VerifyFile reads a backup from disk and verifies it.
func VerifyFile(path string) (Report, []byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Report{}, nil, fmt.Errorf("read backup file: %w", err)
	}
	report, err := VerifyEnvelope(raw)
	if err != nil {
		return Report{}, raw, err
	}
	return report, raw, nil
}
