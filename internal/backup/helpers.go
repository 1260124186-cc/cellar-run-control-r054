package backup

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/storage"
)

// unmarshalStrict rejects unknown fields and trailing data, so a payload cannot
// smuggle fields that would later be silently dropped.
func unmarshalStrict(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.More() {
		return errors.New("backup payload contains trailing data")
	}
	return nil
}

func normalize(snapshot domain.Snapshot) (domain.Snapshot, error) {
	return storage.NormalizeSnapshot(snapshot)
}
