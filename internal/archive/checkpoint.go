package archive

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
)

// checkpointName is the single committed checkpoint file.
const checkpointName = "checkpoint.json"

// Checkpoint records the durable state needed to resume a chunked run:
// every fully committed chunk and the stage it reached.
type Checkpoint struct {
	Stage         string  `json:"stage"`
	Chunks        []Chunk `json:"chunks"`
	CommittedRows int     `json:"committed_rows"`
}

// checkpointPath returns the checkpoint location inside the archive dir.
func checkpointPath(dir string) string {
	return filepath.Join(dir, checkpointName)
}

// writeCheckpointLocked serializes the current chunk list. The caller
// already holds the index lock; the write is atomic so a crash can never
// leave a half-written checkpoint that pretends a chunk is committed.
func (i *Index) writeCheckpointLocked() error {
	cp := Checkpoint{
		Stage:         "archive",
		Chunks:        i.chunks,
		CommittedRows: len(i.rows),
	}
	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(checkpointPath(i.dir), data)
}

// readCheckpoint loads and decodes the checkpoint if one exists. A missing
// file yields (nil, nil) so a fresh run starts cleanly.
func readCheckpoint(dir string) (*Checkpoint, error) {
	data, err := os.ReadFile(checkpointPath(dir))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		// Corrupt checkpoint: treat as absent rather than crash the run.
		return nil, nil
	}
	return &cp, nil
}

// atomicWrite writes data to a temp file in the same directory, fsyncs it,
// then renames it over the target. The rename is atomic on a local
// filesystem, so readers never observe a partially written artifact.
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// sha256Hex returns the hex SHA-256 of data.
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
