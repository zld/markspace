package migrate

import (
	"os"
	"path/filepath"
)

// stageFile writes data to a hidden temporary file in the target's
// directory and flushes it to stable storage. Same-directory staging is
// what makes the later rename atomic.
func stageFile(target string, data []byte) (string, error) {
	dir := filepath.Dir(target)
	f, err := os.CreateTemp(dir, ".migrate-*")
	if err != nil {
		return "", err
	}
	name := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(name)
		return "", err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(name)
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(name)
		return "", err
	}
	return name, nil
}

// commitFile atomically replaces target with the staged file. An
// interruption before the rename leaves the old target fully readable.
func commitFile(tmp, target string) error {
	return os.Rename(tmp, target)
}
