package backup

import (
	"sort"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
)

// Kind identifies the row type shown in a backup inventory.
type Kind string

const (
	KindFormula     Kind = "formula"
	KindVessel      Kind = "vessel"
	KindRun         Kind = "run"
	KindObservation Kind = "observation"
	KindCleaning    Kind = "cleaning"
)

// Item is one inventory row shown before a backup is written.
type Item struct {
	Kind     Kind
	ID       string
	Code     string
	Name     string
	State    string
	Detail   string
	RunCode  string
	Sequence int
}

// Inventory is the complete pre-backup listing.
type Inventory struct {
	Items  []Item
	Counts Counts
}

// CountSnapshot tallies the rows carried by a snapshot.
func CountSnapshot(snapshot domain.Snapshot) Counts {
	counts := Counts{
		Formulas: len(snapshot.Formulas),
		Vessels:  len(snapshot.Vessels),
		Runs:     len(snapshot.Runs),
	}
	for _, run := range snapshot.Runs {
		counts.Observations += len(run.Observations)
	}
	return counts
}

// BuildInventory lists formulas, vessels, runs, cleaning facts, and
// observations in stable operator-readable order.
func BuildInventory(snapshot domain.Snapshot) Inventory {
	inventory := Inventory{Counts: CountSnapshot(snapshot)}

	formulas := make([]domain.Formula, 0, len(snapshot.Formulas))
	for _, formula := range snapshot.Formulas {
		formulas = append(formulas, formula.Clone())
	}
	sort.Slice(formulas, func(i, j int) bool {
		if formulas[i].Name == formulas[j].Name {
			return formulas[i].ID < formulas[j].ID
		}
		return formulas[i].Name < formulas[j].Name
	})
	for _, formula := range formulas {
		inventory.Items = append(inventory.Items, Item{
			Kind:   KindFormula,
			ID:     formula.ID,
			Name:   formula.Name,
			State:  string(formula.State),
			Detail: plural(len(formula.Steps), "step"),
		})
	}

	vessels := make([]domain.Vessel, 0, len(snapshot.Vessels))
	for _, vessel := range snapshot.Vessels {
		vessels = append(vessels, vessel)
	}
	sort.Slice(vessels, func(i, j int) bool {
		if vessels[i].Code == vessels[j].Code {
			return vessels[i].ID < vessels[j].ID
		}
		return vessels[i].Code < vessels[j].Code
	})
	for _, vessel := range vessels {
		runCode := ""
		if vessel.ActiveRunID != nil {
			if run, ok := snapshot.Runs[*vessel.ActiveRunID]; ok {
				runCode = run.Code
			}
		}
		inventory.Items = append(inventory.Items, Item{
			Kind:    KindVessel,
			ID:      vessel.ID,
			Code:    vessel.Code,
			Name:    vessel.Code,
			State:   string(vessel.State),
			RunCode: runCode,
			Detail:  formatCapacity(vessel.CapacityL),
		})
		if vessel.State == domain.VesselCleaning {
			inventory.Items = append(inventory.Items, Item{
				Kind:    KindCleaning,
				ID:      vessel.ID,
				Code:    vessel.Code,
				Name:    vessel.Code,
				State:   string(vessel.State),
				RunCode: runCode,
				Detail:  "vessel awaiting cleaning completion",
			})
		}
	}

	runs := make([]domain.FermentationRun, 0, len(snapshot.Runs))
	for _, run := range snapshot.Runs {
		runs = append(runs, run.Clone())
	}
	sort.Slice(runs, func(i, j int) bool {
		if runs[i].Code == runs[j].Code {
			return runs[i].ID < runs[j].ID
		}
		return runs[i].Code < runs[j].Code
	})
	for _, run := range runs {
		inventory.Items = append(inventory.Items, Item{
			Kind:   KindRun,
			ID:     run.ID,
			Code:   run.Code,
			State:  string(run.State),
			Detail: plural(len(run.Observations), "observation"),
		})
		for _, observation := range run.Observations {
			inventory.Items = append(inventory.Items, Item{
				Kind:     KindObservation,
				ID:       run.ID,
				Code:     run.Code,
				RunCode:  run.Code,
				Sequence: observation.Sequence,
				State:    string(run.State),
				Detail:   formatObservation(observation),
			})
		}
	}
	return inventory
}
