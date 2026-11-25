// Package checkpoint stores per-stage results as content-addressed files.
//
// Each successful stage writes one JSON document named after the task key
// and stage. A document records the input digest it was produced from and
// the SHA-256 of its own payload, so a resumed run can prove that the
// checkpoint it is about to trust was built from the same inputs and was
// not corrupted on disk.
//
// Writes always go to a temporary file in the destination directory and are
// renamed into place, so a killed process leaves either the old document or
// the new one, never a partial one.
package checkpoint

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ErrStale is returned when a checkpoint's recorded input digest differs
// from the caller's expectation.
var ErrStale = errors.New("checkpoint: input digest mismatch")

// ErrCorrupt means the checkpoint's payload does not match its checksum.
var ErrCorrupt = errors.New("checkpoint: payload checksum mismatch")

// ErrMissing means no checkpoint exists for the stage.
var ErrMissing = errors.New("checkpoint: missing")

// Doc is the on-disk envelope. Payload is opaque JSON owned by the stage.
type Doc struct {
	Version     int             `json:"version"`
	TaskKey     string          `json:"task_key"`
	Stage       string          `json:"stage"`
	InputDigest string          `json:"input_digest"`
	PayloadHash string          `json:"payload_hash"`
	Payload     json.RawMessage `json:"payload"`
}

// Sta is the package-level API for a checkpoint directory.
type Sta struct {
	mu  sync.Mutex
	dir string
}

// New returns a store rooted at dir. The directory is created lazily.
func New(dir string) *Sta { return &Sta{dir: dir} }

// Dir returns the root directory.
func (s *Sta) Dir() string { return s.dir }

// Path returns the file path for a task/stage pair.
func (s *Sta) Path(taskKey, stage string) string {
	return filepath.Join(s.dir, sanitize(taskKey)+"."+sanitize(stage)+".ckpt.json")
}

// Write atomically persists payload for stage, tagged with inputDigest.
func (s *Sta) Write(taskKey, stage, inputDigest string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("checkpoint: marshal %s: %w", stage, err)
	}
	sum := sha256.Sum256(body)
	doc := Doc{
		Version:     1,
		TaskKey:     taskKey,
		Stage:       stage,
		InputDigest: inputDigest,
		PayloadHash: hex.EncodeToString(sum[:]),
		Payload:     body,
	}
	data, err := json.MarshalIndent(&doc, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, ".ckpt-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, s.Path(taskKey, stage))
}

// Read loads and verifies a checkpoint. It does not compare digests; use
// Load for the pipeline-facing variant.
func (s *Sta) Read(taskKey, stage string) (*Doc, error) {
	data, err := os.ReadFile(s.Path(taskKey, stage))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s/%s", ErrMissing, taskKey, stage)
	}
	if err != nil {
		return nil, err
	}
	var doc Doc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("checkpoint: decode %s: %w", stage, err)
	}
	// The envelope is written with indentation, which re-indents the
	// embedded payload; compact it back to the exact bytes that were hashed.
	var compact bytes.Buffer
	if err := json.Compact(&compact, doc.Payload); err != nil {
		return nil, fmt.Errorf("%w: %s/%s: %v", ErrCorrupt, taskKey, stage, err)
	}
	sum := sha256.Sum256(compact.Bytes())
	if hex.EncodeToString(sum[:]) != doc.PayloadHash {
		return nil, fmt.Errorf("%w: %s/%s", ErrCorrupt, taskKey, stage)
	}
	return &doc, nil
}

// Load verifies the checkpoint belongs to inputDigest and decodes payload
// into dst.
func (s *Sta) Load(taskKey, stage, inputDigest string, dst any) error {
	doc, err := s.Read(taskKey, stage)
	if err != nil {
		return err
	}
	if doc.InputDigest != inputDigest {
		return fmt.Errorf("%w: %s/%s recorded %s, want %s",
			ErrStale, taskKey, stage, doc.InputDigest, inputDigest)
	}
	if dst == nil || len(doc.Payload) == 0 {
		return nil
	}
	if err := json.Unmarshal(doc.Payload, dst); err != nil {
		return fmt.Errorf("checkpoint: decode payload %s: %w", stage, err)
	}
	return nil
}

// Has reports whether a checkpoint file exists.
func (s *Sta) Has(taskKey, stage string) bool {
	_, err := os.Stat(s.Path(taskKey, stage))
	return err == nil
}

// Remove deletes a checkpoint, missing is not an error.
func (s *Sta) Remove(taskKey, stage string) error {
	err := os.Remove(s.Path(taskKey, stage))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func sanitize(s string) string {
	repl := func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			return r
		default:
			return '_'
		}
	}
	return strings.Map(repl, s)
}
