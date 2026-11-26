package checkpoint

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"

	"archivecheck/internal"
)

// Processing stages persisted in the journal.
const (
	StageIngested  = "ingested"
	StageVerified  = "verified"
	StageCached    = "cached"
	StageCheckpoint = "checkpointed"
	StageIsolated  = "isolated"
	StageRecovered = "recovered"
	StageCompleted = "completed"
)

// Checkpoint is the atomically replaced recovery point.
type Checkpoint struct {
	InputDigest   string `json:"input_digest"`
	Stage         string `json:"stage"`
	Cursor        int64  `json:"cursor"`
	ArchiveSHA256 string `json:"archive_sha256,omitempty"`
	UpdatedNS     int64  `json:"updated_ns,omitempty"`
}

// StageRecord is one append-only processing transition.
type StageRecord struct {
	Offset      int64  `json:"offset"`
	InputDigest string `json:"input_digest"`
	Stage       string `json:"stage"`
	Cursor      int64  `json:"cursor"`
	Source      string `json:"source,omitempty"`
	Detail      string `json:"detail,omitempty"`
}

// Journal owns checkpoint.json and the append-only stage journal.
type Journal struct {
	dir string
	mu  sync.Mutex
}

func Open(dir string) (*Journal, error) {
	if dir == "" {
		return nil, internal.NewFault("checkpoint.init", dir, 0, "journal directory is empty")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, internal.WrapFault("checkpoint.init", dir, 0, err)
	}
	return &Journal{dir: dir}, nil
}

func (j *Journal) CheckpointPath() string {
	return filepath.Join(j.dir, "checkpoint.json")
}

func (j *Journal) JournalPath() string {
	return filepath.Join(j.dir, "stages.jsonl")
}

// Save atomically writes and fsyncs the checkpoint.
func (j *Journal) Save(ctx context.Context, cp Checkpoint) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if cp.InputDigest == "" || cp.Stage == "" || cp.Cursor < 0 {
		return internal.NewFault("checkpoint.save", j.CheckpointPath(), 0,
			"invalid checkpoint: digest=%q stage=%q cursor=%d", cp.InputDigest, cp.Stage, cp.Cursor)
	}
	j.mu.Lock()
	defer j.mu.Unlock()

	b, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return internal.WrapFault("checkpoint.save", j.CheckpointPath(), 0, err)
	}
	if err := atomicWrite(j.CheckpointPath(), append(b, '\n')); err != nil {
		return internal.WrapFault("checkpoint.save", j.CheckpointPath(), 0, err)
	}
	return nil
}

// Load returns found=false when no checkpoint has been written.
func (j *Journal) Load(ctx context.Context) (cp Checkpoint, found bool, err error) {
	if err := ctx.Err(); err != nil {
		return cp, false, err
	}
	b, err := os.ReadFile(j.CheckpointPath())
	if errors.Is(err, os.ErrNotExist) {
		return cp, false, nil
	}
	if err != nil {
		return cp, false, internal.WrapFault("checkpoint.load", j.CheckpointPath(), 0, err)
	}
	if err := json.Unmarshal(b, &cp); err != nil {
		return cp, false, internal.WrapFault("checkpoint.load", j.CheckpointPath(), 0, err)
	}
	return cp, true, nil
}

// AppendStage durably appends a processing record before the atomic checkpoint.
func (j *Journal) AppendStage(ctx context.Context, rec StageRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	f, err := os.OpenFile(j.JournalPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return internal.WrapFault("checkpoint.journal", j.JournalPath(), 0, err)
	}
	defer f.Close()
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if _, err := f.Write(b); err != nil {
		return internal.WrapFault("checkpoint.journal", j.JournalPath(), int(rec.Offset), err)
	}
	return f.Sync()
}

// ReadStages reads all stage records. Corrupt tail lines are reported as faults.
func (j *Journal) ReadStages(ctx context.Context) ([]StageRecord, error) {
	f, err := os.Open(j.JournalPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, internal.WrapFault("checkpoint.replay", j.JournalPath(), 0, err)
	}
	defer f.Close()

	var out []StageRecord
	br := bufio.NewReader(f)
	offset := 0
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			var rec StageRecord
			if jerr := json.Unmarshal(line, &rec); jerr != nil {
				return out, internal.WrapFault("checkpoint.replay", j.JournalPath(), offset, jerr)
			}
			out = append(out, rec)
			offset += len(line)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return out, internal.WrapFault("checkpoint.replay", j.JournalPath(), offset, err)
		}
	}
	return out, nil
}

func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".checkpoint-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
