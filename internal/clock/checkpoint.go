package clock

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

const CheckpointFormat = "medpipe-checkpoint/v1"
const CheckpointVersion = 1

// Checkpoint is the durable resume point. Pipeline-specific analysis state is
// stored as JSON so the clock package need not depend on media packages.
type Checkpoint struct {
	Format            string          `json:"format"`
	CheckpointVersion int             `json:"checkpoint_version"`
	InputSHA256       string          `json:"input_sha256"`
	ParserMajor       int             `json:"parser_major"`
	SSRC              uint32          `json:"ssrc"`
	LastSequence      uint16          `json:"last_sequence"`
	LastExtSequence   uint64          `json:"last_ext_sequence"`
	LastByteOffset    int64           `json:"last_byte_offset"`
	ProcessedPackets  int64           `json:"processed_packets"`
	CheckpointEvery   int             `json:"checkpoint_every"`
	Completed         bool            `json:"completed"`
	OutputSHA256      string          `json:"output_sha256,omitempty"`
	GeneratorConfig   json.RawMessage `json:"generator_config,omitempty"`
	State             json.RawMessage `json:"state,omitempty"`
}

func NewCheckpoint(inputDigest string, parserMajor, every int) *Checkpoint {
	return &Checkpoint{
		Format:            CheckpointFormat,
		CheckpointVersion: CheckpointVersion,
		InputSHA256:       inputDigest,
		ParserMajor:       parserMajor,
		CheckpointEvery:   every,
	}
}

// ValidateResume checks the two invariants required before replaying from a
// checkpoint: identical input bytes and an identical parser major version.
func (c *Checkpoint) ValidateResume(inputDigest string, parserMajor int) *Error {
	if c == nil || c.Format != CheckpointFormat || c.CheckpointVersion != CheckpointVersion {
		return Errorf(CodeCheckpointInvalid, nil, "checkpoint format or version is unsupported")
	}
	if c.InputSHA256 == "" || c.InputSHA256 != inputDigest {
		return Errorf(CodeInputDigestMismatch, &Location{ParserMajor: parserMajor, ByteOffset: c.LastByteOffset},
			"input summary differs from checkpoint: checkpoint=%q current=%q", c.InputSHA256, inputDigest)
	}
	if c.ParserMajor != parserMajor {
		return Errorf(CodeParserMajorMismatch, &Location{ParserMajor: parserMajor},
			"parser major version differs: checkpoint=v%d current=v%d", c.ParserMajor, parserMajor)
	}
	return nil
}

func SaveCheckpoint(path string, cp *Checkpoint) error {
	if cp == nil {
		return Errorf(CodeCheckpointInvalid, nil, "nil checkpoint")
	}
	cp.Format = CheckpointFormat
	cp.CheckpointVersion = CheckpointVersion
	return AtomicWriteJSON(path, cp)
}

func LoadCheckpoint(path string) (*Checkpoint, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, Wrap(CodeCheckpointInvalid, nil, err, "read checkpoint")
	}
	var cp Checkpoint
	if err := json.Unmarshal(b, &cp); err != nil {
		return nil, Wrap(CodeCheckpointInvalid, nil, err, "parse checkpoint")
	}
	if cp.Format != CheckpointFormat || cp.CheckpointVersion != CheckpointVersion {
		return nil, Errorf(CodeCheckpointInvalid, nil, "unsupported checkpoint format/version")
	}
	return &cp, nil
}

// AtomicWriteJSON marshals v and atomically replaces path. Readers either see
// the old complete file or the new complete file, never a partial write.
func AtomicWriteJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	b = append(b, '\n')
	return AtomicWriteFile(path, b, 0o644)
}

// AtomicWriteFile writes in the destination directory, fsyncs the temp file,
// and renames it over path. It never modifies any other path.
func AtomicWriteFile(path string, data []byte, mode fs.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create output directory %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write temporary file %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("sync %s: %w", tmpName, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("chmod %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("atomically replace %s: %w", path, err)
	}
	return nil
}
