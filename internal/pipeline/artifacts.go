package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"

	"archivist/internal/fault"
	"archivist/internal/repair"
	"archivist/internal/scan"
)

// Stage artifacts persist the output of the pure stages. Without them, a
// resumed run would have to recompute scan and repair in memory before it
// could verify later artifacts; with them, resume reads exactly the state the
// interrupted run had produced and nothing more.

// artifactPath derives a stable file name for one stage artifact.
func artifactPath(dir, name, paramsHash, suffix string) string {
	short := paramsHash
	if len(short) > 16 {
		short = short[:16]
	}
	return filepath.Join(dir, name+"-"+short+suffix)
}

// writeJSON writes v atomically and returns its artifact record.
func writeJSON(v any, path string) (Artifact, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Artifact{}, fault.New(fault.KindIO, fault.StageWrite, "create artifact dir").In(path).Wrap(err)
	}
	data, err := json.Marshal(v)
	if err != nil {
		return Artifact{}, fault.New(fault.KindInvariant, fault.StagePipeline, "encode artifact").Wrap(err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return Artifact{}, fault.New(fault.KindIO, fault.StageWrite, "write artifact").In(tmp).Wrap(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return Artifact{}, fault.New(fault.KindIO, fault.StageWrite, "publish artifact").In(path).Wrap(err)
	}
	sum, size, err := hashFile(path)
	if err != nil {
		return Artifact{}, err
	}
	return Artifact{Path: path, Size: size, SHA256: sum}, nil
}

// readJSON verifies the artifact digest before decoding, so a corrupted or
// truncated stage artifact is never mistaken for valid progress.
func readJSON(a Artifact, dst any) error {
	if err := verifyArtifact(a); err != nil {
		return err
	}
	data, err := os.ReadFile(a.Path)
	if err != nil {
		return fault.New(fault.KindIO, fault.StageRead, "read artifact").In(a.Path).Wrap(err)
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return fault.New(fault.KindInvariant, fault.StageRead, "decode artifact").In(a.Path).Wrap(err)
	}
	return nil
}

func writeScanArtifact(c *scan.Container, path string) (Artifact, error) {
	return writeJSON(c, path)
}

func loadScanArtifact(a Artifact) (*scan.Container, error) {
	var c scan.Container
	if err := readJSON(a, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func writeRepairArtifact(r *repair.Result, path string) (Artifact, error) {
	return writeJSON(r, path)
}

func loadRepairArtifact(a Artifact) (*repair.Result, error) {
	var r repair.Result
	if err := readJSON(a, &r); err != nil {
		return nil, err
	}
	return &r, nil
}
