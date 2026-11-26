package checkpoint

import "os"

// writeTrash overwrites a checkpoint file with invalid JSON to simulate a
// torn or corrupt record on disk.
func writeTrash(path string) error {
	return os.WriteFile(path, []byte("{not valid json"), 0o644)
}

// readDirNames returns the base names inside dir.
func readDirNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names, nil
}
