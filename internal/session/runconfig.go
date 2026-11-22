package session

import (
	"encoding/json"
	"os"
	"path/filepath"

	"avcheck/internal/media"
)

// RunFile is the persisted configuration of one run. A verifier re-executes
// from it, so the manifest can be checked against a fresh, independent render.
const RunFile = "run.json"

// RunConfig records everything needed to reproduce a run byte for byte.
type RunConfig struct {
	Version int      `json:"version"`
	Config  Config   `json:"config"`
	Faults  []string `json:"faults"`
	Seed    int64    `json:"seed"`
}

// SaveRun writes run.json into dir.
func SaveRun(dir string, rc RunConfig) error {
	rc.Version = 1
	b, err := json.MarshalIndent(rc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, RunFile), b, 0o644)
}

// LoadRun reads run.json from dir.
func LoadRun(dir string) (RunConfig, error) {
	var rc RunConfig
	b, err := os.ReadFile(filepath.Join(dir, RunFile))
	if err != nil {
		return rc, err
	}
	if err := json.Unmarshal(b, &rc); err != nil {
		return rc, media.NewError(media.KindCorrupt, 0, 0, -1, "run.json: %v", err)
	}
	if rc.Version != 1 {
		return rc, media.NewError(media.KindCorrupt, 0, 0, -1, "run.json version %d", rc.Version)
	}
	return rc, nil
}
