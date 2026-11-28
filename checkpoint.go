package mediakernel

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// CommittedSegment is a fully committed segment plus its output checksum.
type CommittedSegment struct {
	Segment Segment `json:"segment"`
	SHA256  string  `json:"sha256"`
}

// Checkpoint records resumable pipeline state: input digest, stage, committed
// segments, the first unprocessed input offset and detector states.
type Checkpoint struct {
	InputSHA256  string             `json:"inputSha256"`
	Stage        string             `json:"stage"`
	Segments     []CommittedSegment `json:"segments"`
	NextOffset   int64              `json:"nextOffset"`
	Silence      SilenceState       `json:"silence"`
	Fingerprints FingerprintState   `json:"fingerprints"`
}

// LoadCheckpoint reads a checkpoint file; a missing file yields (nil, nil).
func LoadCheckpoint(path string) (*Checkpoint, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c Checkpoint
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("checkpoint %s: %w", path, err)
	}
	return &c, nil
}

// Save atomically writes the checkpoint (temp file + rename).
func (c *Checkpoint) Save(path string) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Validate refuses to resume when the input changed.
func (c *Checkpoint) Validate(inputSHA256 string) error {
	if c.InputSHA256 != inputSHA256 {
		return &Error{ErrInputChanged, 0, "",
			fmt.Sprintf("checkpoint input %s does not match current input %s",
				c.InputSHA256, inputSHA256)}
	}
	return nil
}
