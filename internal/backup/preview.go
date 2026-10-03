package backup

import (
	"sort"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
)

// FormulaSummary describes a formula the backup will contain.
type FormulaSummary struct {
	ID      string
	Name    string
	Style   string
	State   domain.FormulaState
	Steps   int
	Version int64
}

// VesselSummary describes a vessel the backup will contain.
type VesselSummary struct {
	ID          string
	Code        string
	CapacityL   float64
	State       domain.VesselState
	ActiveRunID string
	Version     int64
}

// RunSummary describes a run and its observations.
type RunSummary struct {
	ID             string
	Code           string
	FormulaID      string
	VesselID       string
	State          domain.RunState
	Observations   int
	PlannedVolumeL float64
	Version        int64
}

// CleaningFact describes a vessel currently in the cleaning state, including
// the completed or aborted run that keeps it occupied.
type CleaningFact struct {
	VesselID   string
	VesselCode string
	RunID      string
	RunCode    string
	RunState   domain.RunState
}

// Preview is the complete "what will be backed up" inventory.
type Preview struct {
	Snapshot domain.Snapshot
	Manifest Manifest
	Formulas []FormulaSummary
	Vessels  []VesselSummary
	Runs     []RunSummary
	Cleaning []CleaningFact
}

// BuildPreview inventories every formula, vessel, run, observation count, and
// cleaning fact present in the snapshot.
func BuildPreview(snapshot domain.Snapshot) Preview {
	preview := Preview{
		Snapshot: snapshot,
		Manifest: BuildManifest(snapshot),
		Formulas: make([]FormulaSummary, 0, len(snapshot.Formulas)),
		Vessels:  make([]VesselSummary, 0, len(snapshot.Vessels)),
		Runs:     make([]RunSummary, 0, len(snapshot.Runs)),
		Cleaning: make([]CleaningFact, 0),
	}
	for _, formula := range snapshot.Formulas {
		preview.Formulas = append(preview.Formulas, FormulaSummary{
			ID:      formula.ID,
			Name:    formula.Name,
			Style:   formula.Style,
			State:   formula.State,
			Steps:   len(formula.Steps),
			Version: formula.Version,
		})
	}
	sort.Slice(preview.Formulas, func(i, j int) bool {
		if preview.Formulas[i].Name == preview.Formulas[j].Name {
			return preview.Formulas[i].ID < preview.Formulas[j].ID
		}
		return preview.Formulas[i].Name < preview.Formulas[j].Name
	})

	vesselsByID := make(map[string]domain.Vessel, len(snapshot.Vessels))
	for _, vessel := range snapshot.Vessels {
		summary := VesselSummary{
			ID:        vessel.ID,
			Code:      vessel.Code,
			CapacityL: vessel.CapacityL,
			State:     vessel.State,
			Version:   vessel.Version,
		}
		if vessel.ActiveRunID != nil {
			summary.ActiveRunID = *vessel.ActiveRunID
		}
		preview.Vessels = append(preview.Vessels, summary)
		vesselsByID[vessel.ID] = vessel
	}
	sort.Slice(preview.Vessels, func(i, j int) bool {
		if preview.Vessels[i].Code == preview.Vessels[j].Code {
			return preview.Vessels[i].ID < preview.Vessels[j].ID
		}
		return preview.Vessels[i].Code < preview.Vessels[j].Code
	})

	for _, run := range snapshot.Runs {
		summary := RunSummary{
			ID:             run.ID,
			Code:           run.Code,
			FormulaID:      run.FormulaID,
			State:          run.State,
			Observations:   len(run.Observations),
			PlannedVolumeL: run.PlannedVolumeL,
			Version:        run.Version,
		}
		if run.VesselID != nil {
			summary.VesselID = *run.VesselID
		}
		preview.Runs = append(preview.Runs, summary)

		if run.VesselID != nil {
			if vessel, found := vesselsByID[*run.VesselID]; found &&
				vessel.State == domain.VesselCleaning {
				preview.Cleaning = append(preview.Cleaning, CleaningFact{
					VesselID:   vessel.ID,
					VesselCode: vessel.Code,
					RunID:      run.ID,
					RunCode:    run.Code,
					RunState:   run.State,
				})
			}
		}
	}
	sort.Slice(preview.Runs, func(i, j int) bool {
		if preview.Runs[i].Code == preview.Runs[j].Code {
			return preview.Runs[i].ID < preview.Runs[j].ID
		}
		return preview.Runs[i].Code < preview.Runs[j].Code
	})
	sort.Slice(preview.Cleaning, func(i, j int) bool {
		if preview.Cleaning[i].VesselCode == preview.Cleaning[j].VesselCode {
			return preview.Cleaning[i].RunCode < preview.Cleaning[j].RunCode
		}
		return preview.Cleaning[i].VesselCode < preview.Cleaning[j].VesselCode
	})
	return preview
}
