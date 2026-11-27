package batch

import (
	"os"

	"tracecheck/internal/index"
	"tracecheck/internal/serr"
)

// Resume loads the checkpoint for inputPath and, when valid, returns the
// prefix summary it confirms. A missing checkpoint returns (nil, 0, nil) so a
// caller can simply start from scratch. A corrupt or mismatched checkpoint
// returns an *serr.Error; the caller is expected to report it and restart from
// the last valid checkpoint (or from zero when none is valid).
func Resume(data []byte, workDir, inputName string, opts Options) (*Summary, *index.Checkpoint, error) {
	path := index.CheckpointPath(workDir, inputName)
	prior, err := index.LoadCheckpoint(path)
	if err != nil {
		return nil, nil, err
	}
	if prior == nil {
		return nil, nil, nil
	}
	return nil, prior, nil
}

// Commit persists the summary as a checkpoint atomically.
func Commit(workDir, inputName, inputPath string, sum *Summary) error {
	cp := sum.Checkpoint(inputPath, sum.MetadataDigest)
	if err := index.SaveCheckpoint(index.CheckpointPath(workDir, inputName), cp); err != nil {
		return err
	}
	return nil
}

// EnsureWorkDir creates the working directory if needed.
func EnsureWorkDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return serr.New(serr.CodeIO, "create work dir %s: %v", dir, err)
	}
	return nil
}
