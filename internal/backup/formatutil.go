package backup

import (
	"fmt"
	"strings"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
)

func plural(count int, word string) string {
	if count == 1 {
		return fmt.Sprintf("1 %s", word)
	}
	return fmt.Sprintf("%d %ss", count, word)
}

func formatCapacity(liters float64) string {
	return fmt.Sprintf("%.0f L", liters)
}

func formatObservation(observation domain.GravityObservation) string {
	return fmt.Sprintf("gravity %.3f at %s, %.1f C",
		observation.Gravity,
		observation.ObservedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		observation.TemperatureC)
}

// DefaultFileName builds the deterministic backup file name for a snapshot.
// The same cellar state always maps to the same file name, which makes a
// repeated backup command produce a clear "already exists, verified" result.
func DefaultFileName(snapshot domain.Snapshot) string {
	payload, err := EncodePayload(snapshot)
	if err != nil {
		// EncodePayload only fails on JSON encoding errors of in-memory data,
		// which cannot occur for the domain snapshot types.
		return "cellar-backup-r" + fmt.Sprint(snapshot.Revision) + FileExtension
	}
	sum := digest(payload)
	return fmt.Sprintf("cellar-backup-r%d-%s%s", snapshot.Revision, sum[:12], FileExtension)
}

// ShortDigest returns the first 12 hex characters of a checksum for display.
func ShortDigest(value string) string {
	if len(value) > 12 {
		return value[:12]
	}
	return value
}

// trimToken normalizes a confirmation answer.
func trimToken(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
