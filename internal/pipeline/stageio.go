package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// commitStageJSON writes a stage artifact atomically: temp file first, then a
// rename.  The previous artifact survives a failed run unchanged.
func commitStageJSON(dir, name string, v interface{}) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	buf, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "."+name+".json.tmp")
	if err := os.WriteFile(tmp, append(buf, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, name+".json"))
}

// stage writes to a temp file next to workDir and returns its path.  A failed
// fn leaves the previous committed output untouched.
func stage(workDir, output, name string, fn func(tmp string) error) (string, error) {
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return "", err
	}
	tmp := filepath.Join(workDir, "."+name+"-"+filepath.Base(output)+".tmp")
	if err := fn(tmp); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return tmp, nil
}
