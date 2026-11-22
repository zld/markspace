package pipeline

import (
	"os"
	"path/filepath"

	"mediapipe/internal/cache"
)

func tempDir() (string, error) {
	return os.MkdirTemp("", "mediapipe-faults-*")
}

func writeForgedPayload(root string, key cache.Key) error {
	path := filepath.Join(root, string(key[:2]), string(key)+".bin")
	return os.WriteFile(path, []byte("forged-stage-result"), 0o644)
}
