package backup

import (
	"fmt"
	"sort"
	"time"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
)

// IntegrityStatus summarizes whether an archive is safe to restore.
type IntegrityStatus string

const (
	// IntegrityOK means checksum, structure, and references all agree.
	IntegrityOK IntegrityStatus = "ok"
	// IntegrityInvalid means the payload failed validation and must not restore.
	IntegrityInvalid IntegrityStatus = "invalid"
)

// IntegrityReport explains a verification outcome.
type IntegrityReport struct {
	Status   IntegrityStatus
	Archive  Archive
	Snapshot domain.Snapshot
	Manifest Manifest
	// Errors lists every defect found, so a single run reports all problems.
	Errors []string
}

// Valid reports whether the archive passed.
func (r IntegrityReport) Valid() bool {
	return r.Status == IntegrityOK
}

// Verify proves an archive is complete and internally consistent: checksum,
// payload decode/normalization, manifest counts, entity rules, observation
// sequences, and every cross-entity reference.
func Verify(archive Archive) IntegrityReport {
	report := IntegrityReport{Status: IntegrityOK, Archive: archive}

	if err := archive.VerifyChecksum(); err != nil {
		report.Status = IntegrityInvalid
		report.Errors = append(report.Errors, err.Error())
		return report
	}
	raw, err := archive.PayloadBytes()
	if err != nil {
		report.Status = IntegrityInvalid
		report.Errors = append(report.Errors, err.Error())
		return report
	}

	var snapshot domain.Snapshot
	if err := unmarshalStrict(raw, &snapshot); err != nil {
		report.Status = IntegrityInvalid
		report.Errors = append(report.Errors, fmt.Sprintf("payload is not a valid snapshot: %v", err))
		return report
	}
	normalized, err := normalize(snapshot)
	if err != nil {
		report.Status = IntegrityInvalid
		report.Errors = append(report.Errors, err.Error())
		return report
	}
	report.Snapshot = normalized
	report.Manifest = BuildManifest(normalized)

	errorsList := validateContent(normalized)
	errorsList = append(errorsList, verifyManifest(archive.Manifest, report.Manifest)...)
	if len(errorsList) > 0 {
		report.Status = IntegrityInvalid
		sort.Strings(errorsList)
		report.Errors = append(report.Errors, errorsList...)
	}
	return report
}

func verifyManifest(recorded, actual Manifest) []string {
	var problems []string
	if recorded != actual {
		problems = append(problems, fmt.Sprintf(
			"manifest does not match the payload: recorded formulas=%d vessels=%d runs=%d observations=%d cleaning=%d, actual formulas=%d vessels=%d runs=%d observations=%d cleaning=%d",
			recorded.Formulas, recorded.Vessels, recorded.Runs, recorded.Observations, recorded.Cleaning,
			actual.Formulas, actual.Vessels, actual.Runs, actual.Observations, actual.Cleaning))
	}
	return problems
}

func validateContent(snapshot domain.Snapshot) []string {
	var problems []string

	formulaIDs := make(map[string]domain.Formula, len(snapshot.Formulas))
	for id, formula := range snapshot.Formulas {
		if formula.ID != id {
			problems = append(problems, fmt.Sprintf("formula %s carries a mismatched id %q", id, formula.ID))
		}
		if err := formula.Validate(); err != nil {
			problems = append(problems, fmt.Sprintf("formula %s is invalid: %v", id, err))
		}
		if formula.State == domain.FormulaApproved || formula.State == domain.FormulaRetired {
			if err := domain.ValidateFormulaSteps(formula.Steps, true); err != nil {
				problems = append(problems, fmt.Sprintf("formula %s steps are invalid: %v", id, err))
			}
		}
		formulaIDs[id] = formula
	}

	vesselByID := make(map[string]domain.Vessel, len(snapshot.Vessels))
	for id, vessel := range snapshot.Vessels {
		if vessel.ID != id {
			problems = append(problems, fmt.Sprintf("vessel %s carries a mismatched id %q", id, vessel.ID))
		}
		if err := vessel.Validate(); err != nil {
			problems = append(problems, fmt.Sprintf("vessel %s is invalid: %v", id, err))
		}
		if vessel.ActiveRunID != nil {
			run, found := snapshot.Runs[*vessel.ActiveRunID]
			if !found {
				problems = append(problems, fmt.Sprintf(
					"vessel %s points at missing run %s", id, *vessel.ActiveRunID))
			} else if run.VesselID == nil || *run.VesselID != id {
				problems = append(problems, fmt.Sprintf(
					"vessel %s is linked to run %s but that run does not link back", id, run.ID))
			}
			switch vessel.State {
			case domain.VesselReserved:
				if found && run.State != domain.RunReserved {
					problems = append(problems, fmt.Sprintf(
						"vessel %s is reserved for run %s in state %s", id, run.ID, run.State))
				}
			case domain.VesselInUse:
				if found && run.State != domain.RunFermenting && run.State != domain.RunConditioning {
					problems = append(problems, fmt.Sprintf(
						"vessel %s is in use by run %s in state %s", id, run.ID, run.State))
				}
			case domain.VesselCleaning:
				if found && run.State != domain.RunCompleted && run.State != domain.RunAborted {
					problems = append(problems, fmt.Sprintf(
						"cleaning vessel %s is tied to run %s still in state %s", id, run.ID, run.State))
				}
			case domain.VesselAvailable, domain.VesselRetired:
				problems = append(problems, fmt.Sprintf(
					"vessel %s in state %s must not carry an active run", id, vessel.State))
			default:
				problems = append(problems, fmt.Sprintf("vessel %s has unknown state %q", id, vessel.State))
			}
		} else {
			switch vessel.State {
			case domain.VesselReserved, domain.VesselInUse, domain.VesselCleaning:
				problems = append(problems, fmt.Sprintf(
					"vessel %s in state %s is missing its active run", id, vessel.State))
			case domain.VesselAvailable, domain.VesselRetired:
			default:
				problems = append(problems, fmt.Sprintf("vessel %s has unknown state %q", id, vessel.State))
			}
		}
		vesselByID[id] = vessel
	}

	for id, run := range snapshot.Runs {
		problems = append(problems, validateRun(id, run, snapshot)...)
	}
	return problems
}

func validateRun(id string, run domain.FermentationRun, snapshot domain.Snapshot) []string {
	var problems []string

	if run.ID != id {
		problems = append(problems, fmt.Sprintf("run %s carries a mismatched id %q", id, run.ID))
	}
	if err := run.Validate(); err != nil {
		problems = append(problems, fmt.Sprintf("run %s is invalid: %v", id, err))
	}
	formula, found := snapshot.Formulas[run.FormulaID]
	if !found {
		problems = append(problems, fmt.Sprintf("run %s points at missing formula %s", id, run.FormulaID))
	} else if formula.State != domain.FormulaApproved && formula.State != domain.FormulaRetired {
		problems = append(problems, fmt.Sprintf(
			"run %s is attached to formula %s in state %s, not an approved one",
			id, formula.ID, formula.State))
	}
	if run.VesselID != nil {
		vessel, found := snapshot.Vessels[*run.VesselID]
		if !found {
			problems = append(problems, fmt.Sprintf("run %s points at missing vessel %s", id, *run.VesselID))
		} else if vessel.ActiveRunID == nil || *vessel.ActiveRunID != id {
			problems = append(problems, fmt.Sprintf(
				"run %s claims vessel %s but that vessel does not link back", id, vessel.ID))
		}
	}
	switch run.State {
	case domain.RunPlanned:
		if run.VesselID != nil {
			problems = append(problems, fmt.Sprintf("planned run %s must not reserve a vessel", id))
		}
		if run.StartedAt != nil {
			problems = append(problems, fmt.Sprintf("planned run %s must not have a start time", id))
		}
	case domain.RunReserved:
		if run.VesselID == nil {
			problems = append(problems, fmt.Sprintf("reserved run %s is missing its vessel", id))
		}
		if run.StartedAt != nil {
			problems = append(problems, fmt.Sprintf("reserved run %s must not have a start time", id))
		}
	case domain.RunFermenting:
		if run.VesselID == nil || run.StartedAt == nil {
			problems = append(problems, fmt.Sprintf("fermenting run %s is missing vessel or start time", id))
		}
	case domain.RunConditioning:
		if run.VesselID == nil || run.StartedAt == nil {
			problems = append(problems, fmt.Sprintf("conditioning run %s is missing vessel or start time", id))
		}
		if len(run.Observations) < 2 {
			problems = append(problems, fmt.Sprintf(
				"conditioning run %s has fewer than two observations", id))
		}
	case domain.RunCompleted:
		if run.VesselID == nil || run.StartedAt == nil || run.EndedAt == nil {
			problems = append(problems, fmt.Sprintf("completed run %s is missing vessel or timestamps", id))
		}
		if len(run.Observations) < 3 {
			problems = append(problems, fmt.Sprintf(
				"completed run %s has fewer than three observations", id))
		}
		if run.Metrics == nil {
			problems = append(problems, fmt.Sprintf("completed run %s is missing final metrics", id))
		} else if found {
			expected := domain.CalculateMetrics(
				formula.TargetOriginalGravity,
				run.Observations[len(run.Observations)-1].Gravity,
				len(run.Observations))
			if *run.Metrics != expected {
				problems = append(problems, fmt.Sprintf(
					"completed run %s metrics %+v do not recompute to %+v", id, *run.Metrics, expected))
			}
		}
	case domain.RunAborted:
		if run.EndedAt == nil {
			problems = append(problems, fmt.Sprintf("aborted run %s is missing its end time", id))
		}
	default:
		problems = append(problems, fmt.Sprintf("run %s has unknown state %q", id, run.State))
	}

	startedAt := time.Time{}
	if run.StartedAt != nil {
		startedAt = *run.StartedAt
	}
	if len(run.Observations) > 500 {
		problems = append(problems, fmt.Sprintf(
			"run %s carries %d observations, more than the limit of 500",
			id, len(run.Observations)))
	}
	if run.StartedAt == nil && len(run.Observations) > 0 {
		problems = append(problems, fmt.Sprintf(
			"run %s carries %d observations but was never started",
			id, len(run.Observations)))
	}
	var previous *domain.GravityObservation
	for index := range run.Observations {
		observation := run.Observations[index]
		if observation.RunID != id {
			problems = append(problems, fmt.Sprintf(
				"observation %d of run %s is tagged for run %q",
				observation.Sequence, id, observation.RunID))
		}
		if observation.Sequence != index+1 {
			problems = append(problems, fmt.Sprintf(
				"run %s observation sequences must be contiguous and begin at 1; position %d has sequence %d",
				id, index+1, observation.Sequence))
		}
		if run.StartedAt != nil {
			// ReceivedAt is the most permissive upper bound: historical facts
			// restored on a new machine must not be rejected for being "old".
			if err := domain.ValidateObservation(startedAt, previous, observation, observation.ReceivedAt); err != nil {
				problems = append(problems, fmt.Sprintf(
					"run %s observation %d is invalid: %v", id, observation.Sequence, err))
			}
		}
		previous = &run.Observations[index]
	}
	return problems
}
