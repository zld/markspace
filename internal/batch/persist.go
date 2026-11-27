package batch

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"

	merr "mediatool/internal/errors"
)

// persist writes the checkpoint through a temp file and atomic rename, so a
// crash can never leave a half-written checkpoint that looks valid.
func (c *Checkpoint) persist() error {
	data, err := json.MarshalIndent(c.states, "", "  ")
	if err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if f, err := os.Open(tmp); err == nil {
		_ = f.Sync()
		_ = f.Close()
	}
	return os.Rename(tmp, c.path)
}

// load merges any previously persisted states. Pending entries from the old
// checkpoint stay pending, so only uncommitted work is redone on resume.
func (c *Checkpoint) load() error {
	data, err := os.ReadFile(c.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return merr.New(merr.CodeCheckpointCorrupt, "read checkpoint: %v", err)
	}
	var saved []ChunkState
	if err := json.Unmarshal(data, &saved); err != nil {
		return merr.New(merr.CodeCheckpointCorrupt, "parse checkpoint: %v", err)
	}
	if len(saved) != len(c.states) {
		return merr.New(merr.CodeCheckpointCorrupt,
			"checkpoint has %d chunks, expected %d", len(saved), len(c.states))
	}
	for i, s := range saved {
		if s.ID != i {
			return merr.At(merr.CodeCheckpointCorrupt, int64(i),
				"checkpoint entry %d has id %d", i, s.ID)
		}
		c.states[i] = s
	}
	return nil
}

// verifyFileSHA hashes the output file and compares it to the recorded
// digest. A missing or partial file fails as chunk_incomplete.
func verifyFileSHA(path, want string) error {
	f, err := os.Open(path)
	if err != nil {
		return merr.New(merr.CodeChunkIncomplete, "open %s: %v", path, err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return merr.New(merr.CodeChunkIncomplete, "hash %s: %v", path, err)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != want {
		return merr.New(merr.CodeChunkIncomplete,
			"output %s SHA %s != recorded %s", path, got, want)
	}
	return nil
}
