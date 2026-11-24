// Package engine wires the reusable subsystems into one deterministic
// processing chain: parse -> PSI -> continuity -> timebase -> metadata ->
// index -> segments, driven through the bounded pipeline. The command entry
// point only configures and invokes this package.
package engine

import (
	"context"
	"sync"

	"github.com/example/tsrepair/internal/fixture"
	"github.com/example/tsrepair/internal/index"
	"github.com/example/tsrepair/internal/metadata"
	"github.com/example/tsrepair/internal/model"
	"github.com/example/tsrepair/internal/pipeline"
	"github.com/example/tsrepair/internal/repair"
	"github.com/example/tsrepair/internal/segment"
	"github.com/example/tsrepair/internal/ts"
)

// Config controls one processing run.
type Config struct {
	QueueCapacity      int
	SegmentPackets     int
	Recover            bool
	MaxBufferedPackets int
}

// Metrics is the terminal summary printed by the demo.
type Metrics struct {
	PacketsIn          int
	PacketsAccepted    int
	ParseRoundtripPct  float64
	KeyframeHitRatePct float64
	HashMatchPct       float64
	RepairedWraps      int
	IgnoredDuplicates  int
	ContinuityGaps     int
	PSIVersions        int
	AudioErrors        int
	TruncatedBytes     int
	MaxQueueDepth      int
	SegmentCount       int
	RecoveredDrops     int
	Fatal              *model.StreamError
}

// Engine holds the assembled stages for one run. Stages run on separate
// goroutines, so mu serializes every access to the shared mutable state;
// back-pressure still bounds memory through the pipeline queues.
type Engine struct {
	mu        sync.Mutex
	cfg       Config
	psi       *ts.PSITracker
	cc        *repair.ContinuityChecker
	tsCorr    *repair.TimestampCorrector
	audio     *metadata.AudioNormalizer
	video     *metadata.ColorNormalizer
	builder   *index.Builder
	hasher    *segment.Hasher
	stream    *fixture.Stream
	rawByOrd  map[int64][]byte
	eligible  int // non-null packets that must traverse the pipeline
	audioSeen map[uint16]bool // PIDs whose descriptor was already validated
}

// New assembles an engine for one fixture stream.
func New(cfg Config, stream *fixture.Stream) *Engine {
	if cfg.QueueCapacity < 1 {
		cfg.QueueCapacity = 1
	}
	if cfg.SegmentPackets < 1 {
		cfg.SegmentPackets = 20
	}
	if cfg.MaxBufferedPackets < 1 {
		cfg.MaxBufferedPackets = cfg.QueueCapacity * cfg.SegmentPackets
	}
	return &Engine{
		cfg:      cfg,
		psi:      ts.NewPSITracker(),
		cc:       repair.NewContinuityChecker(),
		tsCorr:   repair.NewTimestampCorrector(),
		audio:    metadata.NewAudioNormalizer(metadata.DefaultAudioConfig()),
		video: metadata.NewColorNormalizer(metadata.ColorConfig{
			DefaultPrimaries: 1, DefaultTransfer: 1, DefaultMatrix: 1}),
		builder:   index.NewBuilder(),
		hasher:    segment.NewHasher(cfg.SegmentPackets),
		stream:    stream,
		rawByOrd:  map[int64][]byte{},
		audioSeen: map[uint16]bool{},
	}
}

// workItem carries one packet through the pipeline stages.
type workItem struct {
	pkt     *ts.Packet
	ordinal int64
	pts     uint64
}

// Run executes the full chain and returns the metrics. Recoverable faults
// are counted and skipped when Recover is enabled; a fatal input error is
// returned for the driver to map to exit code 2.
func (e *Engine) Run(ctx context.Context) (*Metrics, error) {
	source := make(chan pipeline.Item, e.cfg.QueueCapacity)
	runner := pipeline.NewRunner(e.cfg.QueueCapacity, e.cfg.Recover,
		pipeline.Stage{Name: "psi", Run: e.stagePSI},
		pipeline.Stage{Name: "repair", Run: e.stageRepair},
		pipeline.Stage{Name: "metadata", Run: e.stageMetadata},
		pipeline.Stage{Name: "index", Run: e.stageIndex},
	)
	var tr *ts.TruncatedRange
	var pumpErr error
	go func() {
		defer close(source)
		tr, pumpErr = ts.SplitPackets(e.stream.Data, func(p *ts.Packet) error {
			if !p.IsNull() {
				e.mu.Lock()
				e.eligible++
				e.mu.Unlock()
			}
			// The ordinal is the packet's position in the byte stream, so
			// segment ranges line up with rawByOrd even after duplicates or
			// recovered drops.
			ord := p.ByteOffset / int64(ts.PacketSize)
			item := pipeline.Item{
				Seq: ord, Offset: p.ByteOffset,
				Boundary: p.PayloadUnitStart,
				Payload:  workItem{pkt: p, ordinal: ord},
			}
			select {
			case source <- item:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	stats, runErr := runner.Run(ctx, source)
	if tr != nil {
		_ = e.hasher.Flush(int64(e.cfg.SegmentPackets), tr.Offset)
	} else {
		_ = e.hasher.Flush(int64(e.stream.TotalPackets),
			int64(len(e.stream.CompleteData)))
	}
	m, err := e.finalize(stats, tr, runErr)
	// A stage failure cancels ctx, which aborts the parser pump; the
	// classified stage error takes precedence over the pump's ctx error.
	if err == nil && pumpErr != nil && pumpErr != ctx.Err() {
		return m, pumpErr
	}
	return m, err
}
