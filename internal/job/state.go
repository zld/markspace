// Package job defines the segment state machine and its atomically
// persisted JSON manifest.
package job

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Status is the lifecycle state of one segment.
type Status string

const (
	Queued      Status = "queued"
	Running     Status = "running"
	Succeeded   Status = "succeeded"
	Failed      Status = "failed"
	Interrupted Status = "interrupted"
)

// allowed transitions; retry and resume re-enter queued.
var allowed = map[Status][]Status{
	Queued:      {Running, Failed, Interrupted},
	Running:     {Succeeded, Failed, Interrupted},
	Failed:      {Queued},
	Interrupted: {Queued},
	// A succeeded segment may be requeued on resume when its output
	// artifact is missing from disk.
	Succeeded: {Queued},
}

// SegmentState is the persisted record of one segment.
type SegmentState struct {
	Index      int     `json:"index"`
	Status     Status  `json:"status"`
	Attempts   int     `json:"attempts"`
	StartSec   float64 `json:"start_sec"`
	EndSec     float64 `json:"end_sec"`
	TargetKbps float64 `json:"target_kbps"`
	ActualKbps float64 `json:"actual_kbps"`
	PSNRMeanDB float64 `json:"psnr_mean_db"`
	PSNRMinDB  float64 `json:"psnr_min_db"`
	MaxSwingDB float64 `json:"max_swing_db"`
	BelowFloor int     `json:"below_floor_frames"`
	Frames     int     `json:"frames"`
	File       string  `json:"file"`
	Err        string  `json:"err,omitempty"`
}

// State is the whole-run manifest, rewritten atomically on every
// transition so a crash never leaves a torn file.
type State struct {
	Version     int             `json:"version"`
	ConfigHash  string          `json:"config_hash"`
	Config      json.RawMessage `json:"config,omitempty"`
	InputDigest string          `json:"input_digest"`
	Input       string          `json:"input"`
	Segments    []SegmentState  `json:"segments"`
	StartedAt   time.Time       `json:"started_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
	path        string
}

// HashConfig derives the identity of a run's parameters. A resume
// refuses to reuse results when this changes.
func HashConfig(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(b)), nil
}

// New creates a manifest with every segment queued.
func New(configHash, inputDigest, input string, segs []SegmentState) *State {
	now := time.Now().UTC()
	return &State{
		Version:     1,
		ConfigHash:  configHash,
		InputDigest: inputDigest,
		Input:       input,
		Segments:    segs,
		StartedAt:   now,
		UpdatedAt:   now,
	}
}

// Load reads a manifest from disk.
func Load(path string) (*State, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("parse state %s: %w", path, err)
	}
	if s.Version != 1 {
		return nil, fmt.Errorf("unsupported state version %d", s.Version)
	}
	s.path = path
	return &s, nil
}

// Transition moves segment idx to the next state, validating the edge,
// and persists the manifest atomically.
func (s *State) Transition(idx int, to Status, runErr error) error {
	if idx < 0 || idx >= len(s.Segments) {
		return fmt.Errorf("segment index %d out of range", idx)
	}
	seg := &s.Segments[idx]
	ok := false
	for _, t := range allowed[seg.Status] {
		if t == to {
			ok = true
			break
		}
	}
	if !ok {
		return fmt.Errorf("illegal transition %s -> %s for segment %d", seg.Status, to, idx)
	}
	seg.Status = to
	if to == Running {
		seg.Attempts++
	}
	if runErr != nil {
		seg.Err = runErr.Error()
	} else if to != Failed {
		seg.Err = ""
	}
	s.UpdatedAt = time.Now().UTC()
	return s.Save()
}

// Save writes the manifest via temp file + rename inside the same
// directory, so readers never observe a partial write.
func (s *State) Save() error {
	if s.path == "" {
		return fmt.Errorf("state has no path; use SaveAs first")
	}
	return s.SaveAs(s.path)
}

// SaveAs writes the manifest to path atomically.
func (s *State) SaveAs(path string) error {
	s.path = path
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".state-*.json")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Pending returns indexes of segments that still need work: queued,
// failed under the retry cap, or interrupted by a previous run.
func (s *State) Pending(maxRetries int) []int {
	var out []int
	for i, seg := range s.Segments {
		switch seg.Status {
		case Queued, Interrupted:
			out = append(out, i)
		case Failed:
			if seg.Attempts <= maxRetries {
				out = append(out, i)
			}
		}
	}
	return out
}

// Failed returns indexes of segments that ended in a failed state.
func (s *State) Failed() []int {
	var out []int
	for i, seg := range s.Segments {
		if seg.Status == Failed {
			out = append(out, i)
		}
	}
	return out
}

// Done reports whether no segment is queued, running or interrupted.
func (s *State) Done() bool {
	for _, seg := range s.Segments {
		if seg.Status == Queued || seg.Status == Running || seg.Status == Interrupted {
			return false
		}
	}
	return true
}
