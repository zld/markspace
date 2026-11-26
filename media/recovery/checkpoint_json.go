package recovery

import (
	"encoding/json"
	"os"
)

func unmarshalCheckpoint(b []byte, cp *Checkpoint) error {
	return json.Unmarshal(b, cp)
}

// writeCheckpoint serialises deterministically and atomically installs it so
// an interrupted run never leaves a half-written checkpoint.
func writeCheckpoint(path string, cp *Checkpoint) error {
	b, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ReadCheckpointFile is a small exported wrapper for tooling/inspection.
func ReadCheckpointFile(path string) (*Checkpoint, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cp := &Checkpoint{}
	if err := json.Unmarshal(b, cp); err != nil {
		return nil, err
	}
	return cp, nil
}
