// Package store persists job events to a JSONL journal with an atomic
// checkpoint; replaying the journal rebuilds state and requeues survivors.
package store

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Job lifecycle states and journal event kinds.
const (
	StateQueued, StateRunning, StateRetryable, StateCompleted, StateFailed =
		"queued", "running", "retryable", "completed", "failed"
	EvJobCreated, EvClaim, EvRetry, EvCompleted, EvFailed, EvSegmentDone, EvRunInterrupt =
		"job_created", "job_claimed", "job_retry", "job_completed",
		"job_failed", "segment_done", "run_interrupted"
)

// CandidateMetrics holds measured results for one encode candidate.
type CandidateMetrics struct {
	Bytes        int64   `json:"bytes"`
	BitrateKbps  float64 `json:"bitrate_kbps"`
	ErrorPct     float64 `json:"bitrate_error_pct"`
	PSNRMean     float64 `json:"psnr_mean_db"`
	PSNRMin      float64 `json:"psnr_min_db"`
	PSNRMax      float64 `json:"psnr_max_db"`
	PSNRStd      float64 `json:"psnr_std_db"`
	Frames       int     `json:"frames"`
}
type Job struct {
	ID         string           `json:"id"`
	Segment    int              `json:"segment"`
	QP         int              `json:"qp"`
	State      string           `json:"state"`
	Attempts   int              `json:"attempts"`
	MaxAttempt int              `json:"max_attempt"`
	LastError  string           `json:"last_error,omitempty"`
	Metrics    CandidateMetrics `json:"metrics"`
	UpdatedAt  time.Time        `json:"updated_at"`
}
type Event struct {
	Kind      string           `json:"kind"`
	JobID     string           `json:"job_id,omitempty"`
	Segment   int              `json:"segment,omitempty"`
	QP        int              `json:"qp,omitempty"`
	State     string           `json:"state,omitempty"`
	Error     string           `json:"error,omitempty"`
	Metrics   *CandidateMetrics `json:"metrics,omitempty"`
	Timestamp time.Time        `json:"ts"`
}
type Parameters struct {
	SourceFingerprint string  `json:"source_fingerprint"`
	Width             int     `json:"width"`
	Height            int     `json:"height"`
	FPSNum            int     `json:"fps_num"`
	FPSDen            int     `json:"fps_den"`
	Segments          int     `json:"segments"`
	TargetKbps        float64 `json:"target_kbps"`
	FramesPerSegment  int     `json:"frames_per_segment"`
	TotalFrames       int     `json:"total_frames"`
}
type Checkpoint struct {
	Version     int             `json:"version"`
	Params      Parameters      `json:"parameters"`
	Jobs        map[string]*Job `json:"jobs"`
	DoneSegs    map[int]int     `json:"done_segments"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
	Interrupted bool            `json:"interrupted"`
}
var ErrFingerprintMismatch = errors.New("checkpoint parameters do not match; refusing to mix runs")
type Store struct {
	dir       string
	mu        sync.Mutex
	journal   *os.File
	writer    *bufio.Writer
	cp        *Checkpoint
	recovered int
}
const (
	dirMode    = 0o755
	fileMode   = 0o644
	journalName = "events.jsonl"
	cpName      = "checkpoint.json"
)

// Open opens/creates the store; mismatch params give ErrFingerprintMismatch.
func Open(dir string, params Parameters) (*Store, error) {
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return nil, fmt.Errorf("store: mkdir: %w", err)
	}
	jPath := filepath.Join(dir, journalName)
	cpPath := filepath.Join(dir, cpName)
	cp, requeued, err := loadOrInit(cpPath, jPath, params)
	if err != nil {
		return nil, err
	}
	jf, err := os.OpenFile(jPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, fileMode)
	if err != nil {
		return nil, fmt.Errorf("store: open journal: %w", err)
	}
	st := &Store{
		dir:       dir,
		journal:   jf,
		writer:    bufio.NewWriterSize(jf, 1<<16),
		cp:        cp,
		recovered: requeued,
	}
	if requeued > 0 {
		err = st.persistLocked()
	}
	return st, err
}
func loadOrInit(cpPath, jPath string, params Parameters) (*Checkpoint, int, error) {
	raw, err := os.ReadFile(cpPath)
	if errors.Is(err, os.ErrNotExist) {
		return &Checkpoint{Version: 1, Params: params, Jobs: map[string]*Job{},
			CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}, 0, nil
	} else if err != nil {
		return nil, 0, fmt.Errorf("store: read checkpoint: %w", err)
	}
	cp := &Checkpoint{}
	if err := json.Unmarshal(raw, cp); err != nil {
		return nil, 0, fmt.Errorf("store: corrupt checkpoint: %w", err)
	}
	if !paramsEqual(cp.Params, params) {
		return nil, 0, ErrFingerprintMismatch
	}
	cp.Jobs = map[string]*Job{}
	requeued, err := replay(jPath, cp)
	if err != nil {
		return nil, 0, err
	}
	return cp, requeued, nil
}
