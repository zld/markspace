// Package report emits waterline samples and run summaries as JSON Lines.
package report

import (
	"encoding/json"
	"io"
	"sync"
)

// Waterline is one periodic buffer-level sample.
type Waterline struct {
	Type         string `json:"type"`
	Transport    string `json:"transport"`
	TMs          int64  `json:"t_ms"`
	QueuedFrames int    `json:"queued_frames"`
	TargetFrames int    `json:"target_frames"`
}

// Discarded breaks down dropped packets by cause.
type Discarded struct {
	Duplicate uint64 `json:"duplicate"`
	Late      uint64 `json:"late"`
	Overflow  uint64 `json:"overflow"`
	Malformed uint64 `json:"malformed"`
	Evicted   uint64 `json:"evicted"`
}

// Summary is the final per-transport aggregate.
type Summary struct {
	Type               string    `json:"type"`
	Transport          string    `json:"transport"`
	FramesProcessed    uint64    `json:"frames_processed"`
	PlayedFrames       uint64    `json:"played_frames"`
	ConcealedFrames    uint64    `json:"concealed_frames"`
	ReceivedPackets    uint64    `json:"received_packets"`
	DiscardedPackets   Discarded `json:"discarded_packets"`
	PLCContinuityPct   float64   `json:"plc_continuity_pct"`
	PLCBoundaryJumpP95 int       `json:"plc_boundary_jump_p95"`
}

// Emitter writes JSONL records safely from concurrent goroutines.
type Emitter struct {
	mu  sync.Mutex
	enc *json.Encoder
}

func NewEmitter(w io.Writer) *Emitter {
	return &Emitter{enc: json.NewEncoder(w)}
}

func (e *Emitter) Waterline(s Waterline) {
	s.Type = "waterline"
	e.mu.Lock()
	defer e.mu.Unlock()
	_ = e.enc.Encode(s)
}

func (e *Emitter) Summary(s Summary) {
	s.Type = "summary"
	e.mu.Lock()
	defer e.mu.Unlock()
	_ = e.enc.Encode(s)
}
