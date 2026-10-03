package backup

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/storage"
)

// ErrConflicts indicates that a backup and target collide on unique business
// keys with different aggregate ids.
var ErrConflicts = errors.New("backup conflicts with existing target data")

// RestoreResult reports the outcome of a restore.
type RestoreResult struct {
	Plan           Plan
	BackupRevision int64
	SafetyCopyPath string
}

// Preflight verifies the backup file, compares it against the current target
// snapshot, and returns the full impact plan.
func Preflight(backupPath string, store *storage.Store) (Report, Plan, error) {
	report, _, err := VerifyFile(backupPath)
	if err != nil {
		return Report{}, Plan{}, err
	}
	plan := Analyze(report.Snapshot, store.Current())
	return report, plan, nil
}

// RequireNoConflicts rejects a plan when unique keys are owned by different
// ids on target and backup. Such a restore is never a simple overwrite.
func RequireNoConflicts(plan Plan) error {
	if len(plan.Conflicts) == 0 {
		return nil
	}
	messages := make([]string, 0, len(plan.Conflicts))
	for _, conflict := range plan.Conflicts {
		messages = append(messages, "  - "+conflict.Message)
	}
	return fmt.Errorf("%w:\n%s\nresolution requires migrating or removing the target rows first",
		ErrConflicts, strings.Join(messages, "\n"))
}

// Confirm reads an interactive yes/no answer from input. The expected answer
// is the exact token shown in the prompt.
func Confirm(reader io.Reader, writer io.Writer, prompt, expected string) (bool, error) {
	fmt.Fprintf(writer, "%s ", prompt)
	line, err := bufio.NewReader(reader).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("read confirmation: %w", err)
	}
	return trimToken(line) == expected, nil
}

// Apply verifies the backup again and replaces the target snapshot with it.
// The caller is responsible for presenting the plan and obtaining approval.
// Before the durable switch the current state.json is copied to a safety copy,
// and an interrupted attempt never exposes a partial snapshot.
func Apply(backupPath string, store *storage.Store) (RestoreResult, error) {
	report, raw, err := VerifyFile(backupPath)
	if err != nil {
		return RestoreResult{}, err
	}
	plan := Analyze(report.Snapshot, store.Current())
	if err := RequireNoConflicts(plan); err != nil {
		return RestoreResult{}, err
	}

	// Re-read the file immediately before switching so a swapped or edited
	// file cannot be restored after preflight approved different bytes.
	onDisk, err := os.ReadFile(backupPath)
	if err != nil {
		return RestoreResult{}, fmt.Errorf("re-read backup before restore: %w", err)
	}
	if digest(onDisk) != digest(raw) {
		return RestoreResult{}, ErrFileChanged
	}

	candidate, err := DecodePayload(report.Envelope.Payload)
	if err != nil {
		return RestoreResult{}, err
	}
	safetyCopy, err := store.RestoreSnapshot(candidate)
	if err != nil {
		return RestoreResult{}, err
	}
	return RestoreResult{
		Plan:           plan,
		BackupRevision: report.Snapshot.Revision,
		SafetyCopyPath: safetyCopy,
	}, nil
}

// IsInteractive reports whether input and output look interactive.
func IsInteractive(input io.Reader, output io.Writer) bool {
	inputFile, inputOK := input.(*os.File)
	outputFile, outputOK := output.(*os.File)
	if !inputOK || !outputOK {
		return false
	}
	inputInfo, err := inputFile.Stat()
	if err != nil {
		return false
	}
	outputInfo, err := outputFile.Stat()
	if err != nil {
		return false
	}
	return (inputInfo.Mode()&os.ModeCharDevice) != 0 &&
		(outputInfo.Mode()&os.ModeCharDevice) != 0
}

// SnapshotAt returns the UTC snapshot time carried by a report.
func SnapshotAt(report Report) string {
	return report.Envelope.Manifest.SnapshotAt
}

// CreatedAt returns the UTC backup creation time carried by a report.
func CreatedAt(report Report) string {
	return report.Envelope.Manifest.CreatedAt
}

// DigestSummary returns the payload and envelope digests for display.
func DigestSummary(envelope Envelope) (string, string) {
	return envelope.PayloadSHA256, envelope.EnvelopeSHA256
}
