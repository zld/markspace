package remux

import (
	"encoding/json"
	"os"

	"mediacore/internal/fault"
)

type checkpoint struct {
	Version        int    `json:"version"`
	SourceSHA256   string `json:"source_sha256"`
	OutputPath     string `json:"output_path"`
	TempPath       string `json:"temp_path"`
	DataStart      int64  `json:"data_start"`
	TotalPayload   int64  `json:"total_payload"`
	Committed      int    `json:"committed"`
	CommittedBytes int64  `json:"committed_bytes"`
	PlanSHA256     string `json:"plan_sha256"`
}

func loadValidCheckpoint(path, sourceDigest, outPath, tempPath, planHash string) (*checkpoint, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false, nil
	}
	var cp checkpoint
	if json.Unmarshal(data, &cp) != nil || cp.Version != 1 || cp.SourceSHA256 != sourceDigest ||
		cp.OutputPath != outPath || cp.TempPath != tempPath || cp.PlanSHA256 != planHash {
		_ = os.Remove(path)
		_ = os.Remove(tempPath)
		return nil, false, nil
	}
	return &cp, true, nil
}

func saveCheckpoint(path string, cp *checkpoint) error {
	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return fault.Wrap(fault.InvalidArgument, err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fault.At(fault.Wrap(fault.IOFailure, err), path, -1)
	}
	return os.Rename(tmp, path)
}
