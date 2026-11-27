package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"

	"mediasync/internal/model"
)

// ResultName is the conventional result file inside a task directory.
const ResultName = "result.json"

// WriteResult atomically serializes the result and returns its SHA-256.
func WriteResult(dir string, res *Result) (string, error) {
	body, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return "", model.NewError(model.ClassCheckpoint, "encode result: %v", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", model.NewError(model.ClassCheckpoint, "mkdir: %v", err)
	}
	dst := filepath.Join(dir, ResultName)
	tmp, err := os.CreateTemp(dir, ".result-*.tmp")
	if err != nil {
		return "", model.NewError(model.ClassCheckpoint, "temp: %v", err)
	}
	name := tmp.Name()
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return "", model.NewError(model.ClassCheckpoint, "write: %v", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return "", model.NewError(model.ClassCheckpoint, "fsync: %v", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return "", model.NewError(model.ClassCheckpoint, "close: %v", err)
	}
	if err := os.Rename(name, dst); err != nil {
		_ = os.Remove(name)
		return "", model.NewError(model.ClassCheckpoint, "rename: %v", err)
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}
