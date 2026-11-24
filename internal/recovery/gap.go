// Package recovery turns jitter-buffer gaps and corrupted inputs into an
// explicit, auditable record instead of fabricated media.
//
// Two failure classes are kept separate:
//   - gaps (missing packets) are recoverable: the timeline keeps its place by
//     reserving an estimated duration, but no audio samples are invented;
//   - corrupted packets are fatal under the strict policy and abort the run
//     with the structured error preserved for provenance.
package recovery

import (
	"fmt"

	"mediapipeline/internal/clock"
	mpierr "mediapipeline/internal/errors"
	"mediapipeline/internal/jitter"
	"mediapipeline/internal/rtp"
)

// Codec selects the duration estimation for a missing packet.
type Codec uint8

const (
	CodecOpus Codec = iota + 1
	CodecAAC
)

// EstimatedSamples returns the nominal frame length used when a packet is
// missing. Opus fixtures use 20 ms (960 @48k); AAC uses 1024 samples.
func (c Codec) EstimatedSamples() uint16 {
	if c == CodecAAC {
		return 1024
	}
	return 960
}

// Policy selects how failures are handled.
type Policy uint8

const (
	// PolicyStrict aborts on any corrupted packet.
	PolicyStrict Policy = iota
	// PolicySkipGaps recovers gaps but still aborts on corruption.
	PolicySkipGaps
)

// Config configures the Recoverer.
type Config struct {
	Codec      Codec
	Policy     Policy
	Node       string
	SampleRate uint32
}

// Outcome is the recovered result for one jitter Event.
type Outcome struct {
	// Seq is the extended sequence position this outcome covers.
	Seq uint64
	// Frame is the original frame for a delivered packet, nil for a gap.
	Frame *rtp.Frame
	// Gap is the recorded gap when this outcome covers a missing packet.
	Gap *GapRecord
	// Interval is the timeline interval reserved for this outcome.
	Interval clock.Interval
}

// GapRecord is the auditable record of one recovered gap.
type GapRecord struct {
	Seq              uint64
	EstimatedSamples uint64
	Ticks            uint64
	Node             string
}

// Recoverer processes jitter events against the shared timeline.
type Recoverer struct {
	cfg Config
	tl  *clock.Timeline
	log []GapRecord
	cur uint64 // next audio tick to allocate
}

// New builds a Recoverer bound to the given timeline.
func New(cfg Config, tl *clock.Timeline) (*Recoverer, error) {
	if cfg.SampleRate == 0 {
		return nil, fmt.Errorf("recovery: sample rate must be non-zero")
	}
	if cfg.Codec == 0 {
		cfg.Codec = CodecOpus
	}
	return &Recoverer{cfg: cfg, tl: tl}, nil
}

// Gaps returns a copy of every gap recorded so far.
func (r *Recoverer) Gaps() []GapRecord {
	out := make([]GapRecord, len(r.log))
	copy(out, r.log)
	return out
}

// Handle maps one jitter event to a recovered outcome, committing exactly one
// immutable timeline interval per sequence position.
func (r *Recoverer) Handle(ev jitter.Event) (Outcome, error) {
	if ev.Frame != nil {
		return r.handleFrame(ev.Frame)
	}
	return r.handleGap(ev.Gap)
}

func (r *Recoverer) handleFrame(f *rtp.Frame) (Outcome, error) {
	ticks := r.tl.Convert(uint64(f.Samples), uint64(r.cfg.SampleRate))
	if ticks == 0 {
		return Outcome{}, mpierr.New(mpierr.CatLimit, f.ExtSeq, r.cfg.Node,
			"frame maps to zero timeline ticks").AtOffset(f.StreamOffset)
	}
	start := r.cur
	iv, err := r.tl.Commit(clock.KindAudio, start, ticks)
	if err != nil {
		return Outcome{}, mpierr.New(mpierr.CatSync, f.ExtSeq, r.cfg.Node,
			"timeline commit rejected").AtOffset(f.StreamOffset).Wrap(err)
	}
	r.cur = iv.End
	return Outcome{Seq: f.ExtSeq, Frame: f, Interval: iv}, nil
}

func (r *Recoverer) handleGap(g *jitter.Gap) (Outcome, error) {
	if r.cfg.Policy == PolicyStrict {
		return Outcome{}, mpierr.New(mpierr.CatGap, g.Seq, r.cfg.Node,
			"strict policy forbids gap recovery")
	}
	samples := uint64(r.cfg.Codec.EstimatedSamples())
	ticks := r.tl.Convert(samples, uint64(r.cfg.SampleRate))
	start := r.cur
	// Reserve silence-shaped space without inventing any samples: mux fills
	// the interval with explicit silence, not reconstructed audio.
	iv, err := r.tl.Commit(clock.KindAudio, start, ticks)
	if err != nil {
		return Outcome{}, mpierr.New(mpierr.CatSync, g.Seq, r.cfg.Node,
			"gap timeline commit rejected").Wrap(err)
	}
	r.cur = iv.End
	rec := GapRecord{Seq: g.Seq, EstimatedSamples: samples, Ticks: ticks, Node: r.cfg.Node}
	r.log = append(r.log, rec)
	return Outcome{Seq: g.Seq, Gap: &rec, Interval: iv}, nil
}

// RejectCorrupt wraps a corruption error under the configured policy. Both
// policies treat corruption as fatal; the node name is normalized.
func (r *Recoverer) RejectCorrupt(e *mpierr.MediaError) *mpierr.MediaError {
	return e.WithNode(r.cfg.Node)
}
