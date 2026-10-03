package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/storage"
)

// ChangeKind classifies what a restore does to one target entity.
type ChangeKind string

const (
	// ChangeIdentical means target and backup already hold the same record.
	ChangeIdentical ChangeKind = "identical"
	// ChangeAdded means the target has no record with this ID.
	ChangeAdded ChangeKind = "added"
	// ChangeOverwritten means the same ID holds different data.
	ChangeOverwritten ChangeKind = "overwritten"
	// ChangeRemoved means the target record is absent from the backup and the
	// restore deletes it.
	ChangeRemoved ChangeKind = "removed"
)

// EntityChange is one line in the restore scope report.
type EntityChange struct {
	Kind   ChangeKind
	ID     string
	Label  string
	Detail string
}

// ConflictKind distinguishes harmless identity collisions from cross-record
// unique-key collisions.
type ConflictKind string

const (
	// ConflictOverwrittenKey means the same unique key is held by the same ID,
	// whose content differs; the replacement overwrites it.
	ConflictOverwrittenKey ConflictKind = "overwritten-key"
	// ConflictUniqueKeyCollision means two different IDs on the target claim a
	// unique key; the non-backup record is removed by the replacement.
	ConflictUniqueKeyCollision ConflictKind = "unique-key-collision"
)

// Conflict describes a unique-key clash a restore must resolve.
type Conflict struct {
	Kind       ConflictKind
	Domain     string
	Key        string
	BackupID   string
	TargetID   string
	Resolution string
}

// Plan is the complete restore scope, computed without mutating anything.
type Plan struct {
	Backup     domain.Snapshot
	Target     domain.Snapshot
	TargetHash string
	BackupHash string
	Manifest   Manifest
	Formulas   []EntityChange
	Vessels    []EntityChange
	Runs       []EntityChange
	Conflicts  []Conflict
}

// Counts summarizes the plan for quick confirmation.
type Counts struct {
	Formulas     map[ChangeKind]int
	Vessels      map[ChangeKind]int
	Runs         map[ChangeKind]int
	Observations int
}

// HasChanges reports whether anything at all would differ after switching.
func (p Plan) HasChanges() bool {
	for _, changes := range [][]EntityChange{p.Formulas, p.Vessels, p.Runs} {
		for _, change := range changes {
			if change.Kind != ChangeIdentical {
				return true
			}
		}
	}
	return false
}

// Counts tallies the plan.
func (p Plan) Counts() Counts {
	counts := Counts{
		Formulas: map[ChangeKind]int{},
		Vessels:  map[ChangeKind]int{},
		Runs:     map[ChangeKind]int{},
	}
	for _, change := range p.Formulas {
		counts.Formulas[change.Kind]++
	}
	for _, change := range p.Vessels {
		counts.Vessels[change.Kind]++
	}
	for _, change := range p.Runs {
		counts.Runs[change.Kind]++
	}
	counts.Observations = p.Manifest.Observations
	return counts
}

// BuildPlan compares a verified backup against the current target snapshot.
func BuildPlan(backup, target domain.Snapshot) (Plan, error) {
	targetHash, err := storage.SnapshotHash(target)
	if err != nil {
		return Plan{}, err
	}
	backupHash, err := storage.SnapshotHash(backup)
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{
		Backup:     backup,
		Target:     target,
		TargetHash: targetHash,
		BackupHash: backupHash,
		Manifest:   BuildManifest(backup),
		Formulas:   diffFormulas(backup, target),
		Vessels:    diffVessels(backup, target),
		Runs:       diffRuns(backup, target),
		Conflicts:  findConflicts(backup, target),
	}
	return plan, nil
}

func diffFormulas(backup, target domain.Snapshot) []EntityChange {
	changes := make([]EntityChange, 0)
	seen := map[string]bool{}
	for id, entity := range backup.Formulas {
		seen[id] = true
		kind := ChangeAdded
		detail := "state=" + string(entity.State)
		if current, found := target.Formulas[id]; found {
			if entityDigest(entity) == entityDigest(current) {
				kind = ChangeIdentical
			} else {
				kind = ChangeOverwritten
				detail = "state " + string(current.State) + " -> " + string(entity.State)
			}
		}
		changes = append(changes, EntityChange{
			Kind:   kind,
			ID:     id,
			Label:  entity.Name,
			Detail: detail,
		})
	}
	for id, entity := range target.Formulas {
		if seen[id] {
			continue
		}
		changes = append(changes, EntityChange{
			Kind:   ChangeRemoved,
			ID:     id,
			Label:  entity.Name,
			Detail: "state=" + string(entity.State),
		})
	}
	sortChanges(changes)
	return changes
}

func diffVessels(backup, target domain.Snapshot) []EntityChange {
	changes := make([]EntityChange, 0)
	seen := map[string]bool{}
	for id, entity := range backup.Vessels {
		seen[id] = true
		kind := ChangeAdded
		detail := "state=" + string(entity.State)
		if current, found := target.Vessels[id]; found {
			if entityDigest(entity) == entityDigest(current) {
				kind = ChangeIdentical
			} else {
				kind = ChangeOverwritten
				detail = "state " + string(current.State) + " -> " + string(entity.State)
			}
		}
		changes = append(changes, EntityChange{
			Kind:   kind,
			ID:     id,
			Label:  entity.Code,
			Detail: detail,
		})
	}
	for id, entity := range target.Vessels {
		if seen[id] {
			continue
		}
		changes = append(changes, EntityChange{
			Kind:   ChangeRemoved,
			ID:     id,
			Label:  entity.Code,
			Detail: "state=" + string(entity.State),
		})
	}
	sortChanges(changes)
	return changes
}

func diffRuns(backup, target domain.Snapshot) []EntityChange {
	changes := make([]EntityChange, 0)
	seen := map[string]bool{}
	for id, entity := range backup.Runs {
		seen[id] = true
		kind := ChangeAdded
		detail := runDetail(entity)
		if current, found := target.Runs[id]; found {
			if entityDigest(entity) == entityDigest(current) {
				kind = ChangeIdentical
			} else {
				kind = ChangeOverwritten
				detail = "state " + string(current.State) + " -> " + string(entity.State) +
					"; observations " + strconv.Itoa(len(current.Observations)) + " -> " +
					strconv.Itoa(len(entity.Observations))
			}
		}
		changes = append(changes, EntityChange{
			Kind:   kind,
			ID:     id,
			Label:  entity.Code,
			Detail: detail,
		})
	}
	for id, entity := range target.Runs {
		if seen[id] {
			continue
		}
		changes = append(changes, EntityChange{
			Kind:   ChangeRemoved,
			ID:     id,
			Label:  entity.Code,
			Detail: runDetail(entity),
		})
	}
	sortChanges(changes)
	return changes
}

func runDetail(run domain.FermentationRun) string {
	return "state=" + string(run.State) + "; observations=" + strconv.Itoa(len(run.Observations))
}

func findConflicts(backup, target domain.Snapshot) []Conflict {
	conflicts := make([]Conflict, 0)

	for key, backupID := range backup.NameIndex {
		targetID, found := target.NameIndex[key]
		if !found {
			continue
		}
		backupEntity := backup.Formulas[backupID]
		if targetID == backupID {
			targetEntity := target.Formulas[targetID]
			if entityDigest(backupEntity) != entityDigest(targetEntity) {
				conflicts = append(conflicts, Conflict{
					Kind:       ConflictOverwrittenKey,
					Domain:     "formula",
					Key:        key,
					BackupID:   backupID,
					TargetID:   targetID,
					Resolution: "overwrite the existing formula",
				})
			}
			continue
		}
		conflicts = append(conflicts, Conflict{
			Kind:       ConflictUniqueKeyCollision,
			Domain:     "formula",
			Key:        key,
			BackupID:   backupID,
			TargetID:   targetID,
			Resolution: "remove target formula " + targetID + " and install " + backupID,
		})
	}

	for key, backupID := range backup.VesselIndex {
		targetID, found := target.VesselIndex[key]
		if !found {
			continue
		}
		if targetID == backupID {
			if entityDigest(backup.Vessels[backupID]) != entityDigest(target.Vessels[targetID]) {
				conflicts = append(conflicts, Conflict{
					Kind:       ConflictOverwrittenKey,
					Domain:     "vessel",
					Key:        key,
					BackupID:   backupID,
					TargetID:   targetID,
					Resolution: "overwrite the existing vessel",
				})
			}
			continue
		}
		conflicts = append(conflicts, Conflict{
			Kind:       ConflictUniqueKeyCollision,
			Domain:     "vessel",
			Key:        key,
			BackupID:   backupID,
			TargetID:   targetID,
			Resolution: "remove target vessel " + targetID + " and install " + backupID,
		})
	}

	for key, backupID := range backup.RunIndex {
		targetID, found := target.RunIndex[key]
		if !found {
			continue
		}
		if targetID == backupID {
			if entityDigest(backup.Runs[backupID]) != entityDigest(target.Runs[targetID]) {
				conflicts = append(conflicts, Conflict{
					Kind:       ConflictOverwrittenKey,
					Domain:     "run",
					Key:        key,
					BackupID:   backupID,
					TargetID:   targetID,
					Resolution: "overwrite the existing run including its observations",
				})
			}
			continue
		}
		conflicts = append(conflicts, Conflict{
			Kind:       ConflictUniqueKeyCollision,
			Domain:     "run",
			Key:        key,
			BackupID:   backupID,
			TargetID:   targetID,
			Resolution: "remove target run " + targetID + " and install " + backupID,
		})
	}

	sort.Slice(conflicts, func(i, j int) bool {
		if conflicts[i].Domain != conflicts[j].Domain {
			return conflicts[i].Domain < conflicts[j].Domain
		}
		if conflicts[i].Key != conflicts[j].Key {
			return conflicts[i].Key < conflicts[j].Key
		}
		return conflicts[i].TargetID < conflicts[j].TargetID
	})
	return conflicts
}

func sortChanges(changes []EntityChange) {
	sort.Slice(changes, func(i, j int) bool {
		rank := map[ChangeKind]int{
			ChangeOverwritten: 0,
			ChangeRemoved:     1,
			ChangeAdded:       2,
			ChangeIdentical:   3,
		}
		if rank[changes[i].Kind] != rank[changes[j].Kind] {
			return rank[changes[i].Kind] < rank[changes[j].Kind]
		}
		if changes[i].Label != changes[j].Label {
			return changes[i].Label < changes[j].Label
		}
		return changes[i].ID < changes[j].ID
	})
}

// entityDigest is a content digest per entity. JSON round-tripping first
// removes nil-versus-empty-slice noise so semantically equal records compare
// equal even when decoded through different paths.
func entityDigest(entity any) string {
	encoded, err := json.Marshal(entity)
	if err != nil {
		panic(err)
	}
	var roundTripped any
	if err := json.Unmarshal(encoded, &roundTripped); err != nil {
		panic(err)
	}
	canonical, err := json.Marshal(roundTripped)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}
