package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// deltaState persists the incremental counters across the interrupt and resume
// invocations so the reported numbers describe the whole resumable run rather
// than resetting when the process exits and restarts.
type deltaState struct {
	// InterruptedAt is the number of assets durably completed before the
	// cooperative stop.
	InterruptedAt int `json:"interrupted_at"`
	// Reused counts assets whose verified checkpoint let a later run skip
	// recomputation; Recomputed counts assets processed from source bytes.
	Reused     int `json:"reused"`
	Recomputed int `json:"recomputed"`
}

func deltaStatePath(dir string) string { return filepath.Join(dir, ".delta.json") }

func loadDeltaState(dir string) (*deltaState, error) {
	data, err := os.ReadFile(deltaStatePath(dir))
	if os.IsNotExist(err) {
		return &deltaState{}, nil
	}
	if err != nil {
		return nil, err
	}
	var s deltaState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func saveDeltaState(dir string, s *deltaState) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".delta-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, deltaStatePath(dir))
}
