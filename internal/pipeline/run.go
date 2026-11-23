// Package pipeline orchestrates the kernel modules into one deterministic
// discrete-event run: parse, sequence, buffer, recover, watermark, replay,
// sync and index.
package pipeline

import (
	"container/heap"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"mediad/internal/buffer"
	"mediad/internal/index"
	"mediad/internal/model"
	"mediad/internal/recovery"
	"mediad/internal/rtp"
	"mediad/internal/sync"
	"mediad/internal/timeline"
)

// Config parameterizes one run. Negative Loss/ReorderMs keep the
// fixture's own values.
type Config struct {
	Seed         int64
	Fixture      string
	Loss         float64
	ReorderMs    int64
	LateWindowMs int64
	CacheSize    int
	ReportEvery  int
}

type seqKey struct {
	ssrc uint32
	seq  uint64
}

// lossInfo tracks one sequence gap from detection to resolution.
type lossInfo struct {
	estEventMs int64
	declared   bool
}

type sim struct {
	spec    Spec
	parser  rtp.Parser
	ext     map[uint32]*rtp.Extender
	mediaOf map[uint32]model.MediaType
	jit     *buffer.Jitter
	wm      *timeline.Watermark
	replay  *timeline.Replay
	rec     *recovery.Recoverer
	est     *sync.Estimator
	q       chunkHeap
	counter int
	depth   int64

	next     map[uint32]uint64
	nextSet  map[uint32]bool
	lastExt  map[uint32]uint64
	lastEvMs map[uint32]int64
	stepMs   map[uint32]int64

	missing  map[seqKey]*lossInfo
	accepted map[seqKey]bool
	emitted  map[seqKey]bool
	quarant  int

	latencies   []int64
	sum         model.Summary
	onEmit      func(emitted int)
	reportEvery int
}

// Run executes one deterministic pipeline run and returns its summary.
// progress, if non-nil, is invoked every ReportEvery emitted samples.
func Run(cfg Config, progress func(emitted int)) (model.Summary, error) {
	var spec Spec
	found := false
	for _, f := range Fixtures() {
		if f.Name == cfg.Fixture {
			spec, found = f, true
		}
	}
	if !found {
		return model.Summary{}, fmt.Errorf("unknown fixture %q", cfg.Fixture)
	}
	if cfg.Loss >= 0 {
		spec.Loss = cfg.Loss
	}
	if cfg.ReorderMs >= 0 {
		spec.ReorderMs = cfg.ReorderMs
	}
	if spec.Loss < 0 || spec.Loss > 1 {
		return model.Summary{}, fmt.Errorf("loss %.3f out of range [0,1]", spec.Loss)
	}
	lateWindow := cfg.LateWindowMs
	if lateWindow <= 0 {
		lateWindow = 250
	}
	cacheSize := cfg.CacheSize
	if cacheSize <= 0 {
		cacheSize = 512
	}
	chunks, truth := Generate(spec, cfg.Seed)

	depth := spec.ReorderMs + 40
	s := &sim{
		spec:        spec,
		ext:         make(map[uint32]*rtp.Extender),
		mediaOf:     make(map[uint32]model.MediaType),
		jit:         buffer.NewJitter(depth),
		wm:          timeline.NewWatermark(depth, lateWindow),
		replay:      timeline.NewReplay(),
		rec:         recovery.NewRecoverer(recovery.NewCache(cacheSize)),
		est:         sync.NewEstimator(),
		depth:       depth,
		next:        make(map[uint32]uint64),
		nextSet:     make(map[uint32]bool),
		lastExt:     make(map[uint32]uint64),
		lastEvMs:    make(map[uint32]int64),
		stepMs:      make(map[uint32]int64),
		missing:     make(map[seqKey]*lossInfo),
		accepted:    make(map[seqKey]bool),
		emitted:     make(map[seqKey]bool),
		onEmit:      progress,
		reportEvery: cfg.ReportEvery,
	}
	s.sum.Fixture = spec.Name
	s.sum.MalformedByClass = make(map[string]int)
	s.sum.QuarantineReasons = make(map[string]int)
	// The sender-side retransmit cache is populated from the transmit log,
	// not from what survived the network. The log also identifies each
	// SSRC's media type, which feedback may reference before media arrives.
	for _, sent := range truth.Sent {
		ev := sent.Event
		ev.ArrivalMs = sent.AtMs
		s.rec.Offer(&ev)
		if ev.Media != model.MediaUnknown {
			s.mediaOf[ev.SSRC] = ev.Media
		}
	}
	for _, c := range chunks {
		s.push(c.ArrivalMs, c.Data)
	}
	s.drain()
	for _, ev := range s.parser.Finish() {
		s.handleMalformed(&ev)
	}
	for _, ev := range s.jit.Flush() {
		s.emit(ev)
	}
	s.finish(truth)
	return s.sum, nil
}

func (s *sim) push(at int64, data []byte) {
	s.counter++
	heap.Push(&s.q, &Chunk{ArrivalMs: at, Data: data, order: s.counter})
}

// drain pops chunks in (arrival, insertion) order until the queue is empty.
func (s *sim) drain() {
	for s.q.Len() > 0 {
		c := heap.Pop(&s.q).(*Chunk)
		for _, ev := range s.parser.Feed(c.Data, c.ArrivalMs) {
			s.handleEvent(&ev)
		}
		s.wm.Advance(c.ArrivalMs)
		s.declare()
		for _, ev := range s.jit.PopReady(s.wm.Current()) {
			s.emit(ev)
		}
	}
}

func (s *sim) finish(truth Truth) {
	// Recovery is scored against exactly the packets the network dropped,
	// so feedback for primed-but-dropped rows cannot inflate the rate.
	recovered := 0
	for _, key := range truth.LostKeys {
		if s.rec.Stats.RecoveredKeys[key] {
			recovered++
		}
	}
	s.sum.LostPackets = len(truth.LostKeys)
	s.sum.RecoveredPackets = recovered
	if s.sum.LostPackets > 0 {
		s.sum.RecoveryRate = float64(recovered) / float64(s.sum.LostPackets)
	} else {
		s.sum.RecoveryRate = 1
	}
	s.sum.CacheMiss = s.rec.Stats.CacheMiss
	s.sum.DupNACK = s.rec.Stats.DupNACK
	s.sum.LateFeedback = s.rec.Stats.LateFeedback
	s.sum.LateReplayed = s.replay.LateReplayed
	s.sum.LateDropped += s.replay.LateDropped
	s.sum.QuarantinedPackets = s.quarant

	res := s.est.Estimate()
	s.sum.AVSyncErrorMs = res.ErrAbs()
	s.sum.AVSyncConfidence = res.Confidence
	s.sum.AVSyncSamples = res.Paired
	s.sum.ClockBiasMs = res.ClockBiasMs
	s.sum.ClockDriftPPM = res.DriftPPM
	s.sum.OutOfSyncSegments = res.OutOfTolSegs
	s.sum.SyncMalformed = s.est.Malformed
	s.sum.P95LatencyMs = percentile(s.latencies, 0.95)

	out := s.replay.Outputs()
	s.sum.EmittedSamples = len(out)
	var video []*model.Event
	for _, ev := range out {
		if ev.Media == model.MediaVideo {
			video = append(video, ev)
		}
	}
	idx := index.Rebuild(video)
	s.sum.IndexRebuilt = len(idx.Keyframes)
	s.sum.RecoverableSegments = idx.Recoverable
	s.sum.UnrecoverableSegs = idx.Unrecoverable
	s.sum.IndexComplete = idx.Unrecoverable == 0
	s.sum.ResultSHA256 = digest(s.sum)
}

// percentile is the nearest-rank percentile of a copy of values.
func percentile(values []int64, q float64) int64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, k int) bool { return sorted[i] < sorted[k] })
	rank := int(q*float64(len(sorted)) + 0.999999)
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

// canonicalJSON marshals the summary with the digest field blanked.
func canonicalJSON(sum model.Summary) []byte {
	sum.ResultSHA256 = ""
	b, _ := json.Marshal(sum)
	return b
}

func digest(sum model.Summary) string {
	h := sha256.Sum256(canonicalJSON(sum))
	return hex.EncodeToString(h[:])
}

// chunkHeap orders chunks by (ArrivalMs, insertion order).
type chunkHeap []*Chunk

func (h chunkHeap) Len() int { return len(h) }
func (h chunkHeap) Less(i, j int) bool {
	if h[i].ArrivalMs != h[j].ArrivalMs {
		return h[i].ArrivalMs < h[j].ArrivalMs
	}
	return h[i].order < h[j].order
}
func (h chunkHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *chunkHeap) Push(x any)   { *h = append(*h, x.(*Chunk)) }
func (h *chunkHeap) Pop() any {
	old := *h
	n := len(old)
	c := old[n-1]
	*h = old[:n-1]
	return c
}
