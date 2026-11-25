package rtp

import (
	"github.com/pion/rtp"

	"mediasync/internal/model"
)

// CaptureFrame is one raw datagram as captured, in arrival order.
type CaptureFrame struct {
	Data       []byte
	ByteOffset int64
	Arrival    int
}

// RecoveryConfig bundles the reorder and jitter parameters for one stream.
type RecoveryConfig struct {
	Reorder ReorderConfig
	Jitter  JitterConfig
}

// DefaultRecoveryConfig returns the composed offline defaults.
func DefaultRecoveryConfig() RecoveryConfig {
	return RecoveryConfig{Reorder: DefaultReorderConfig(), Jitter: DefaultJitterConfig()}
}

// RecoveryStats is the quantitative summary of one recovery run. It feeds
// the report subsystem and must be deterministic for identical inputs.
type RecoveryStats struct {
	TotalPackets  int            `json:"total_packets"`
	Corrupt       int            `json:"corrupt_packets"`
	Duplicates    int            `json:"duplicates"`
	Late          int            `json:"late"`
	Missing       int            `json:"missing_packets"`
	Emitted       int            `json:"emitted_packets"`
	HighWater     int            `json:"buffer_high_water"`
	Gaps          []Gap          `json:"gaps"`
	JitterTarget  int            `json:"jitter_target"`
	JitterMin     int            `json:"jitter_min_level"`
	JitterMax     int            `json:"jitter_max_level"`
	SmoothedBPS   int64          `json:"smoothed_bps"`
}

// Recovery is the stateful, single-goroutine recovery engine. The pipeline
// drives it one datagram at a time and reads ordered packets out via Drain.
type Recovery struct {
	cfg     RecoveryConfig
	rec     *model.Recorder
	reorder *ReorderBuffer
	jitter  *JitterBuffer
	prevSeq uint64
	seen    bool
	stats   RecoveryStats
}

// NewRecovery builds an engine sharing the pipeline error recorder.
func NewRecovery(cfg RecoveryConfig, rec *model.Recorder) *Recovery {
	return &Recovery{
		cfg:     cfg,
		rec:     rec,
		reorder: NewReorderBuffer(cfg.Reorder, rec),
		jitter:  NewJitterBuffer(cfg.Jitter),
	}
}

// Ingest parses one captured datagram. A corrupt frame is recorded with its
// byte offset and dropped (never fabricated); valid packets are extended and
// handed to the reorder buffer. It returns false when the frame was corrupt
// or rejected for capacity, so callers can account for it.
func (g *Recovery) Ingest(f CaptureFrame) bool {
	g.stats.TotalPackets++
	var pkt rtp.Packet
	if err := pkt.Unmarshal(f.Data); err != nil {
		g.stats.Corrupt++
		g.rec.Add(model.ErrorRecord{Code: model.ErrCorruptPacket, Track: "rtp",
			Offset: f.ByteOffset, Message: err.Error()})
		return false
	}
	ext := ExtendSeq(g.prevSeq, g.seen, pkt.SequenceNumber)
	g.prevSeq, g.seen = ext, true
	ok, err := g.reorder.Push(Packet{
		Raw: &pkt, ExtSeq: ext, Arrival: f.Arrival, ByteOffset: f.ByteOffset,
	})
	if err != nil {
		return false
	}
	g.jitter.SetLevel(len(g.reorder.buf))
	return ok
}

// ObserveBandwidth forwards a bandwidth sample to the adaptive jitter buffer.
func (g *Recovery) ObserveBandwidth(s BandwidthSample) {
	g.jitter.Observe(s)
}

// Pump drains everything currently deliverable and closes wait-window gaps.
// Call it after Ingest (and once more with force=true at end of stream).
func (g *Recovery) Pump(force bool) []Packet {
	out := g.reorder.Drain()
	g.reorder.FlushExpired(force)
	out = append(out, g.reorder.Drain()...)
	g.jitter.SetLevel(len(g.reorder.buf))
	return out
}

// Stats snapshots the counters without mutating recovery state.
func (g *Recovery) Stats() RecoveryStats {
	g.stats.Duplicates = g.reorder.Duplicates
	g.stats.Late = g.reorder.Late
	g.stats.Missing = g.reorder.MissingCount()
	g.stats.Emitted = g.reorder.Emitted
	g.stats.HighWater = g.reorder.HighWater
	g.stats.Gaps = g.reorder.Gaps
	g.stats.JitterTarget = g.jitter.Target()
	g.stats.JitterMin = g.jitter.MinLevel
	g.stats.JitterMax = g.jitter.MaxLevel
	g.stats.SmoothedBPS = g.jitter.SmoothedBandwidth()
	return g.stats
}

// RecoverStream runs an entire capture through the engine and returns the
// fully ordered packet sequence plus the summary. All datagrams are ingested
// first (so adjacent duplicates are still in the reorder buffer when their
// copy arrives), then the buffer is pumped to close wait-window gaps and
// drain in order, with a final forced flush at end of stream. Corrupt frames
// are reported on rec; unrecovered gaps are declared, never filled with fake
// media. It is the convenience entry point used by the demo pipeline.
func RecoverStream(frames []CaptureFrame, bandwidth []BandwidthSample,
	cfg RecoveryConfig, rec *model.Recorder) ([]Packet, RecoveryStats) {
	g := NewRecovery(cfg, rec)
	for i, f := range frames {
		if f.Arrival == 0 && i > 0 {
			f.Arrival = i
		}
		g.Ingest(f)
		if i < len(bandwidth) {
			g.ObserveBandwidth(bandwidth[i])
		}
	}
	// Periodically pump while gaps age: each pass lets the wait window
	// advance, but the buffered packets are only emitted as they become
	// contiguous, preserving in-buffer duplicate detection.
	ordered := g.Pump(true)
	return ordered, g.Stats()
}
