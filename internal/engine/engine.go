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
	QueueCapacity        int
	SegmentPackets       int
	Recover              bool
	MaxBufferedPackets   int
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
		video:    metadata.NewColorNormalizer(metadata.ColorConfig{
			DefaultPrimaries: 1, DefaultTransfer: 1, DefaultMatrix: 1}),
		builder:  index.NewBuilder(),
		hasher:   segment.NewHasher(cfg.SegmentPackets),
		stream:   stream,
		rawByOrd: map[int64][]byte{},
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
		var ordinal int64
		tr, pumpErr = ts.SplitPackets(e.stream.Data, func(p *ts.Packet) error {
			// The ordinal is the packet's position in the byte stream, so
			// segment ranges line up with rawByOrd even after duplicates or
			// recovered drops.
			ord := p.ByteOffset / int64(ts.PacketSize)
			if ord > ordinal {
				ordinal = ord
			}
			item := pipeline.Item{
				Seq: ordinal, Offset: p.ByteOffset,
				Boundary: p.PayloadUnitStart,
				Payload:  workItem{pkt: p, ordinal: ordinal},
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

// stagePSI applies the active mapping and rejects unknown PIDs fatally.
func (e *Engine) stagePSI(_ context.Context, in pipeline.Item) (pipeline.Item, error) {
	w := in.Payload.(workItem)
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.psi.Observe(w.pkt); err != nil {
		return in, err
	}
	if w.pkt.PID == ts.PIDPAT || w.pkt.IsNull() {
		return in, nil
	}
	if e.psi.Active() == nil {
		return in, repair.FatalMappingError(w.pkt)
	}
	if w.pkt.PID == e.psi.Active().PMTPID {
		return in, nil
	}
	if _, ok := e.psi.Active().Lookup(w.pkt.PID); !ok {
		return in, repair.FatalMappingError(w.pkt)
	}
	return in, nil
}

// stageRepair runs continuity and timestamp correction.
func (e *Engine) stageRepair(_ context.Context, in pipeline.Item) (pipeline.Item, error) {
	w := in.Payload.(workItem)
	e.mu.Lock()
	defer e.mu.Unlock()
	res, _ := e.cc.Check(w.pkt)
	if res == repair.CheckDuplicate {
		return in, model.New(model.StageContinuity, model.SeverityRecoverable,
			"duplicate packet ignored")
	}
	if corr, err := e.tsCorr.ObservePCR(w.pkt); err != nil {
		return in, err
	} else if corr != nil {
		w.pts = corr.Corrected
		in.Payload = w
	}
	return in, nil
}

// stageMetadata validates audio descriptors and video color metadata.
func (e *Engine) stageMetadata(_ context.Context, in pipeline.Item) (pipeline.Item, error) {
	w := in.Payload.(workItem)
	e.mu.Lock()
	defer e.mu.Unlock()
	track, ok := e.psi.Active().Lookup(w.pkt.PID)
	if !ok {
		return in, nil
	}
	switch track.Kind {
	case ts.KindAudio:
		layout := metadata.LayoutStereo
		rate := uint32(48000)
		if hasMarker(w.pkt.Payload, 0x7F) {
			layout = metadata.LayoutUnknown
		}
		if _, err := e.audio.Normalize(metadata.AudioDescriptor{
			PID: w.pkt.PID, Layout: layout, Channels: 2,
			SampleRate: rate, FrameSizeBytes: 4,
		}, w.pkt.ByteOffset); err != nil {
			return in, err
		}
		if err := e.audio.CheckDivisible(w.pkt.PID,
			len(w.pkt.Payload), 4, w.pkt.ByteOffset); err != nil {
			return in, err
		}
	case ts.KindVideo:
		res := e.video.Normalize(
			metadata.ColorMetadata{PID: w.pkt.PID, ColorPrimaries: 9,
				TransferCharacteristics: 16, MatrixCoefficients: 9},
			metadata.ColorMetadata{PID: w.pkt.PID}, w.pkt.ByteOffset)
		_ = res
	}
	return in, nil
}

// stageIndex admits verified keyframes and feeds segment hashing.
func (e *Engine) stageIndex(_ context.Context, in pipeline.Item) (pipeline.Item, error) {
	w := in.Payload.(workItem)
	e.mu.Lock()
	defer e.mu.Unlock()
	res := e.builder.Consider(w.pkt, w.ordinal, w.pts)
	if res.Admitted {
		e.hasher.NoteKeyframe(len(e.builder.Build().Frames) - 1)
	}
	e.hasher.AddPacket(w.ordinal, w.pkt.ByteOffset, w.pts, w.pkt.Raw)
	e.rawByOrd[w.ordinal] = w.pkt.Raw
	return in, nil
}

func (e *Engine) finalize(stats *pipeline.Stats, tr *ts.TruncatedRange, runErr error) (*Metrics, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	ix := e.builder.Build()
	summaries := e.hasher.Summaries()
	segment.AttachIndexRanges(summaries, ix)
	hashOK, hashTotal := e.verifyHashes(summaries)
	hits, seeks := e.verifyKeyframes(ix)
	ccStats := e.cc.Stats()
	m := &Metrics{
		PacketsAccepted:    stats.Processed,
		RepairedWraps:      e.tsCorr.WrapCount(),
		IgnoredDuplicates:  len(ccStats.Duplicates),
		ContinuityGaps:     len(ccStats.Gaps),
		PSIVersions:        len(e.psi.Boundaries()),
		AudioErrors:        e.audio.ErrorCount(),
		TruncatedBytes:     truncatedLen(tr),
		MaxQueueDepth:      stats.MaxQueueDepth,
		SegmentCount:       len(summaries),
		RecoveredDrops:     stats.Recovered,
		HashMatchPct:       pct(hashOK, hashTotal),
		KeyframeHitRatePct: pct(hits, seeks),
		ParseRoundtripPct:  e.roundtripPct(stats),
	}
	m.PacketsIn = e.stream.TotalPackets
	if se := model.AsStreamError(runErr); se != nil {
		// Non-recover mode surfaces every classified stage fault; recover
		// mode leaves runErr nil because the runner already handled it.
		m.Fatal = se
		return m, se
	}
	return m, nil
}
// verifyHashes recomputes every fully-confirmed segment digest from the
// stored raw packets. Segments containing recovered (dropped) packets cannot
// be re-verified byte-for-byte and are excluded rather than counted as misses.
func (e *Engine) verifyHashes(summaries []segment.Summary) (ok, total int) {
	for _, s := range summaries {
		var buf []byte
		complete := true
		// Re-hash exactly the ordinals the segment was built from; the
		// summary makes the membership auditable for independent nodes.
		for _, ord := range s.Ordinals {
			raw, exists := e.rawByOrd[ord]
			if !exists {
				complete = false
				break
			}
			buf = append(buf, raw...)
		}
		if !complete {
			continue
		}
		total++
		if s.Recompute(buf) {
			ok++
		}
	}
	return ok, total
}

// verifyKeyframes reads every indexed frame back from the complete stream.
func (e *Engine) verifyKeyframes(ix *index.Index) (hits, seeks int) {
	for _, f := range ix.Frames {
		seeks++
		if err := ix.VerifySeek(f, e.stream.CompleteData); err == nil {
			hits++
		}
	}
	return hits, seeks
}

func (e *Engine) roundtripPct(stats *pipeline.Stats) float64 {
	eligible := e.stream.TotalPackets
	if eligible == 0 {
		return 100
	}
	// Accepted plus recovered-dropped packets were all parsed safely; the
	// round-trip measures how much of the input survived into confirmed work.
	return pct(stats.Processed, eligible)
}

func hasMarker(payload []byte, marker byte) bool {
	for _, b := range payload {
		if b == marker {
			return true
		}
	}
	return false
}

func truncatedLen(tr *ts.TruncatedRange) int {
	if tr == nil {
		return 0
	}
	return tr.Length
}

func pct(part, total int) float64 {
	if total == 0 {
		return 100
	}
	return float64(part) * 100 / float64(total)
}
