// Package metrics aggregates playout water levels, loss-concealment
// behaviour and protocol counters for the JSON Lines report.
package metrics

import (
	"errors"

	"jitterlab/internal/buffer"
	"jitterlab/internal/conceal"
	"jitterlab/internal/packet"
)

// Frame is one playout slot as reported to the user.
type Frame struct {
	Frame      int    `json:"frame"`
	TimeMs     int64  `json:"t_ms"`
	Seq        uint64 `json:"seq"`
	Kind       string `json:"kind"`
	BufferMs   int    `json:"buffer_ms"`
	TargetMs   int    `json:"target_ms"`
	ConcealRun int    `json:"conceal_run,omitempty"`
}

// Protocol counts how datagrams were classified by the decoder.
type Protocol struct {
	Input          int `json:"input_packets"`
	Valid          int `json:"valid_packets"`
	Invalid        int `json:"invalid_packets"`
	Truncated      int `json:"truncated_packets"`
	UnknownVersion int `json:"version_errors"`
	BadLength      int `json:"length_errors"`
	Checksum       int `json:"checksum_errors"`
	Consecutive    int `json:"consecutive_invalid"` // longest consecutive run
	current        int
}

// Summary is the aggregated result of one run.
type Summary struct {
	Frames           int       `json:"frames"`
	InputPackets     int       `json:"input_packets"`
	ValidPackets     int       `json:"valid_packets"`
	InvalidPackets   int       `json:"invalid_packets"`
	DuplicatePackets int       `json:"duplicate_packets"`
	ReorderedPackets int       `json:"reordered_packets"`
	DroppedPackets   int       `json:"dropped_packets"`
	ConcealedFrames  int       `json:"concealed_frames"`
	SilentFrames     int       `json:"silent_frames"`
	MaxConcealRun    int       `json:"max_conceal_run"`
	Recoveries       int       `json:"recoveries"`
	ContinuityDelta  float64   `json:"continuity_delta"`
	ConcealRatio     float64   `json:"conceal_ratio"`
	BufferMsMin      int       `json:"buffer_ms_min"`
	BufferMsMax      int       `json:"buffer_ms_max"`
	BufferMsAvg      float64   `json:"buffer_ms_avg"`
	TargetMs         int       `json:"target_ms"`
	MaxWaitMs        int       `json:"max_wait_ms"`
	WindowSize       int       `json:"reorder_window"`
	FrameMs          int       `json:"frame_ms"`
	Seed             int64     `json:"seed"`
	Truncated        int       `json:"truncated_packets"`
	VersionErrors    int       `json:"version_errors"`
	LengthErrors     int       `json:"length_errors"`
	ChecksumErrors   int       `json:"checksum_errors"`
	LongestBadRun    int       `json:"longest_bad_run"`
	RecoveryJumps    []float64 `json:"-"`
}

// Recorder accumulates statistics over one receive path. It is owned by a
// single receive loop and is not safe for concurrent use.
type Recorder struct {
	cfg   buffer.Config
	proto Protocol
	seed  int64

	frames    int
	real      int
	concealed int
	silent    int
	run       int
	maxRun    int

	bufferSum  int64
	bufferMin  int
	bufferMax  int
	haveBuffer bool

	recoveries int
	continuity float64
}

// New builds a recorder for the given buffer configuration and run seed.
func New(cfg buffer.Config, seed int64) *Recorder {
	return &Recorder{cfg: cfg, seed: seed}
}

// ObserveDatagram records one datagram and its decode result. A nil error
// means the packet decoded successfully.
func (r *Recorder) ObserveDatagram(size int, err error) {
	r.proto.Input++
	if err == nil {
		r.proto.Valid++
		r.proto.current = 0
		return
	}
	r.proto.Invalid++
	r.proto.current++
	if r.proto.current > r.proto.Consecutive {
		r.proto.Consecutive = r.proto.current
	}
	switch {
	case errors.Is(err, packet.ErrTruncated):
		r.proto.Truncated++
	case errors.Is(err, packet.ErrUnknownVersion):
		r.proto.UnknownVersion++
	case errors.Is(err, packet.ErrBadLength):
		r.proto.BadLength++
	case errors.Is(err, packet.ErrChecksum):
		r.proto.Checksum++
	}
	_ = size
}

// ObserveSlot folds one playout slot into the aggregates and returns its
// per-frame record.
func (r *Recorder) ObserveSlot(slot buffer.Slot) Frame {
	r.frames++
	switch slot.Kind {
	case conceal.KindReal:
		r.real++
		if slot.Recovered {
			r.recoveries++
			r.continuity += slot.Delta
		}
	case conceal.KindSilence:
		r.concealed++
		r.silent++
	default:
		r.concealed++
	}

	r.run = slot.ConcealRun
	if r.run > r.maxRun {
		r.maxRun = r.run
	}
	r.bufferSum += int64(slot.BufferMs)
	if !r.haveBuffer || slot.BufferMs < r.bufferMin {
		r.bufferMin = slot.BufferMs
	}
	if !r.haveBuffer || slot.BufferMs > r.bufferMax {
		r.bufferMax = slot.BufferMs
	}
	r.haveBuffer = true

	return Frame{
		Frame:      r.frames,
		TimeMs:     slot.TimeMs,
		Seq:        slot.Seq,
		Kind:       slot.Kind.String(),
		BufferMs:   slot.BufferMs,
		TargetMs:   slot.TargetMs,
		ConcealRun: slot.ConcealRun,
	}
}

// Protocol returns a copy of the datagram classification counters.
func (r *Recorder) Protocol() Protocol { return r.proto }

// Summary produces the run aggregate. st carries counters raised inside
// the shared controller (duplicates, reordering, drops).
func (r *Recorder) Summary(st buffer.Stats) Summary {
	s := Summary{
		Frames:           r.frames,
		InputPackets:     r.proto.Input,
		ValidPackets:     r.proto.Valid,
		InvalidPackets:   r.proto.Invalid,
		DuplicatePackets: st.Duplicates,
		ReorderedPackets: st.Reordered,
		DroppedPackets:   st.Dropped,
		ConcealedFrames:  r.concealed,
		SilentFrames:     r.silent,
		MaxConcealRun:    r.maxRun,
		Recoveries:       r.recoveries,
		MaxWaitMs:        r.cfg.MaxWaitMs,
		WindowSize:       r.cfg.WindowSize,
		FrameMs:          r.cfg.FrameMs,
		Seed:             r.seed,
		Truncated:        r.proto.Truncated,
		VersionErrors:    r.proto.UnknownVersion,
		LengthErrors:     r.proto.BadLength,
		ChecksumErrors:   r.proto.Checksum,
		LongestBadRun:    r.proto.Consecutive,
		BufferMsMin:      r.bufferMin,
		BufferMsMax:      r.bufferMax,
	}
	if r.recoveries > 0 {
		s.ContinuityDelta = r.continuity / float64(r.recoveries)
	}
	if r.frames > 0 {
		s.ConcealRatio = float64(r.concealed) / float64(r.frames)
		s.BufferMsAvg = float64(r.bufferSum) / float64(r.frames)
	}
	return s
}
