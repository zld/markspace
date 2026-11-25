package checkpoint

import (
	"os"
	"path/filepath"
)

func writeFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o644)
}

func mkdirAll(path string) error {
	return os.MkdirAll(path, 0o755)
}

// readDirTemp returns an error when no .tmp file remains under dir.
func readDirTemp(dir string) (string, error) {
	var found string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && filepath.Ext(path) == tmpSuffix {
			found = path
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", os.ErrNotExist
	}
	return found, nil
}
