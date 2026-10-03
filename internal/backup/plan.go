package backup

import (
	"fmt"
	"reflect"
	"sort"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
)

// Action describes how one target row is treated by a restore.
type Action string

const (
	// ActionIdentical means the backup row and the target row already match.
	ActionIdentical Action = "identical"
	// ActionAdd means the row exists only in the backup and will be inserted.
	ActionAdd Action = "add"
	// ActionOverwrite means the same id exists with different content.
	ActionOverwrite Action = "overwrite"
	// ActionDelete means the row exists only on the target and will be removed
	// because a restore replaces the whole snapshot.
	ActionDelete Action = "delete"
)

// Scope identifies which collection a plan change belongs to.
type Scope string

const (
	ScopeFormula     Scope = "formula"
	ScopeVessel      Scope = "vessel"
	ScopeRun         Scope = "run"
	ScopeObservation Scope = "observation"
)

// Change is one row-level consequence of restoring the backup.
type Change struct {
	Scope    Scope
	Action   Action
	ID       string
	Code     string
	Name     string
	Detail   string
	RunCode  string
	Affected int
}

// Conflict describes a hard collision where two different ids own the same
// unique business key on target and backup.
type Conflict struct {
	Scope   Scope
	Key     string
	Backup  string
	Target  string
	Message string
}

// Plan is the full pre-restore impact assessment.
type Plan struct {
	Changes          []Change
	Conflicts        []Conflict
	Identical        bool
	Adds             int
	Overwrites       int
	Deletes          int
	ObservationDelta int
}

// HasChanges reports whether the restore would alter anything.
func (p Plan) HasChanges() bool {
	return p.Adds != 0 || p.Overwrites != 0 || p.Deletes != 0 || p.ObservationDelta != 0
}

// Analyze compares a verified backup snapshot with the current target
// snapshot and reports every overwrite and the full replacement scope.
func Analyze(backup, target domain.Snapshot) Plan {
	plan := Plan{Changes: make([]Change, 0)}

	plan.Conflicts = append(plan.Conflicts, findKeyConflicts(ScopeFormula,
		backup.Formulas, target.Formulas,
		func(formula domain.Formula) string { return domain.NameKey(formula.Name) })...)
	plan.Conflicts = append(plan.Conflicts, findKeyConflicts(ScopeVessel,
		backup.Vessels, target.Vessels,
		func(vessel domain.Vessel) string { return vessel.Code })...)
	plan.Conflicts = append(plan.Conflicts, findKeyConflicts(ScopeRun,
		backup.Runs, target.Runs,
		func(run domain.FermentationRun) string { return run.Code })...)

	formulaIDs := sortedKeys(backup.Formulas, target.Formulas)
	for _, id := range formulaIDs {
		backupFormula, inBackup := backup.Formulas[id]
		targetFormula, inTarget := target.Formulas[id]
		switch {
		case inBackup && !inTarget:
			plan.add(ScopeFormula, ActionAdd, id, "", backupFormula.Name,
				plural(len(backupFormula.Steps), "step"), len(backupFormula.Steps))
		case inBackup && inTarget:
			if !reflect.DeepEqual(backupFormula, targetFormula) {
				plan.add(ScopeFormula, ActionOverwrite, id, "", backupFormula.Name,
					"formula content differs", len(backupFormula.Steps))
			} else {
				plan.add(ScopeFormula, ActionIdentical, id, "", backupFormula.Name, "", len(backupFormula.Steps))
			}
		case !inBackup && inTarget:
			plan.add(ScopeFormula, ActionDelete, id, "", targetFormula.Name,
				"present only on target", len(targetFormula.Steps))
		}
	}

	vesselIDs := sortedKeys(backup.Vessels, target.Vessels)
	for _, id := range vesselIDs {
		backupVessel, inBackup := backup.Vessels[id]
		targetVessel, inTarget := target.Vessels[id]
		switch {
		case inBackup && !inTarget:
			plan.add(ScopeVessel, ActionAdd, id, backupVessel.Code, backupVessel.Code,
				string(backupVessel.State), 0)
		case inBackup && inTarget:
			if !reflect.DeepEqual(backupVessel, targetVessel) {
				plan.add(ScopeVessel, ActionOverwrite, id, backupVessel.Code, backupVessel.Code,
					fmt.Sprintf("state %s -> %s", targetVessel.State, backupVessel.State), 0)
			} else {
				plan.add(ScopeVessel, ActionIdentical, id, backupVessel.Code, backupVessel.Code, "", 0)
			}
		case !inBackup && inTarget:
			plan.add(ScopeVessel, ActionDelete, id, targetVessel.Code, targetVessel.Code,
				"present only on target", 0)
		}
	}

	runIDs := sortedKeys(backup.Runs, target.Runs)
	for _, id := range runIDs {
		backupRun, inBackup := backup.Runs[id]
		targetRun, inTarget := target.Runs[id]
		switch {
		case inBackup && !inTarget:
			plan.add(ScopeRun, ActionAdd, id, backupRun.Code, backupRun.Code,
				string(backupRun.State), len(backupRun.Observations))
			plan.ObservationDelta += len(backupRun.Observations)
		case inBackup && inTarget:
			if !reflect.DeepEqual(backupRun, targetRun) {
				delta := len(backupRun.Observations) - len(targetRun.Observations)
				plan.add(ScopeRun, ActionOverwrite, id, backupRun.Code, backupRun.Code,
					fmt.Sprintf("state %s -> %s, observations %d -> %d",
						targetRun.State, backupRun.State,
						len(targetRun.Observations), len(backupRun.Observations)),
					len(backupRun.Observations))
				plan.ObservationDelta += delta
			} else {
				plan.add(ScopeRun, ActionIdentical, id, backupRun.Code, backupRun.Code, "",
					len(backupRun.Observations))
			}
		case !inBackup && inTarget:
			plan.add(ScopeRun, ActionDelete, id, targetRun.Code, targetRun.Code,
				"present only on target", len(targetRun.Observations))
			plan.ObservationDelta -= len(targetRun.Observations)
		}
	}

	plan.Identical = !plan.HasChanges() && len(plan.Conflicts) == 0
	return plan
}

func (p *Plan) add(scope Scope, action Action, id, code, name, detail string, affected int) {
	switch action {
	case ActionAdd:
		p.Adds++
	case ActionOverwrite:
		p.Overwrites++
	case ActionDelete:
		p.Deletes++
	}
	p.Changes = append(p.Changes, Change{
		Scope:    scope,
		Action:   action,
		ID:       id,
		Code:     code,
		Name:     name,
		Detail:   detail,
		Affected: affected,
	})
}

func findKeyConflicts[T any](
	scope Scope,
	backup map[string]T,
	target map[string]T,
	keyOf func(T) string,
) []Conflict {
	backupByKey := map[string]string{}
	for id, value := range backup {
		backupByKey[keyOf(value)] = id
	}
	targetByKey := map[string]string{}
	for id, value := range target {
		targetByKey[keyOf(value)] = id
	}
	keys := make([]string, 0)
	for key := range backupByKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	conflicts := make([]Conflict, 0)
	for _, key := range keys {
		backupID := backupByKey[key]
		targetID, exists := targetByKey[key]
		if exists && backupID != targetID {
			conflicts = append(conflicts, Conflict{
				Scope:  scope,
				Key:    key,
				Backup: backupID,
				Target: targetID,
				Message: fmt.Sprintf(
					"%s key %q is owned by backup id %s but target id %s",
					scope, key, backupID, targetID),
			})
		}
	}
	return conflicts
}

func sortedKeys[V any](maps ...map[string]V) []string {
	seen := map[string]struct{}{}
	for _, entries := range maps {
		for key := range entries {
			seen[key] = struct{}{}
		}
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
