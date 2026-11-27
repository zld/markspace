package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"

	"mediad/internal/fixture"
	"mediad/internal/model"
)

func parseU64(v, flag string) (uint64, error) {
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return 0, model.ErrUsage.At("", 0, "cli", 0, flag+" expects an unsigned integer")
	}
	return n, nil
}

func parseI64(v, flag string) (int64, error) {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, model.ErrUsage.At("", 0, "cli", 0, flag+" expects an integer")
	}
	return n, nil
}

func parseFloat(v, flag string) (float64, error) {
	n, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, model.ErrUsage.At("", 0, "cli", 0, flag+" expects a number")
	}
	return n, nil
}

// storedCapture mirrors pipeline.storedCapture just enough to recover the
// codec on resume.
type storedCapture struct {
	Spec struct {
		ID string `json:"ID"`
	} `json:"Spec"`
}

// detectScenario recovers the scenario by matching the persisted ingest
// artifact's track against the fixture specs.
func detectScenario(outDir string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(outDir, "artifacts", "ingest.packets"))
	if err != nil {
		return "", model.ErrStateCorrupt.At("", 0, "resume", 0,
			"cannot find ingest artifact in "+outDir)
	}
	var cap storedCapture
	if err := json.Unmarshal(raw, &cap); err != nil {
		return "", model.ErrStateCorrupt.At("", 0, "resume", 0, err.Error())
	}
	if cap.Spec.ID == "aac-a" {
		return fixture.ScenTruncAAC, nil
	}
	return fixture.ScenHealthy, nil
}
