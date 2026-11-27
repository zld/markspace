// Package fanout runs independent per-representation clock and index
// recovery. Each representation owns its own PCR/PTS state, error collector
// and random-access index, so corruption in one bitrate cannot propagate to
// another.
package fanout

import (
	"tsrepair/internal/clock"
	"tsrepair/internal/index"
	"tsrepair/internal/mediaerr"
	"tsrepair/internal/segment"
)

// Config tunes one representation's independent recovery.
type Config struct {
	KeyframePID        uint16
	MaxDriftCorrection int64 // 27 MHz ticks, single-step cap
	OutlierTicks       int64
	Duplicates         clock.DuplicatePolicy
}

// DefaultConfig returns conservative recovery limits.
func DefaultConfig(pid uint16) Config {
	return Config{
		KeyframePID:        pid,
		MaxDriftCorrection: 27_000, // at most 1 ms of correction per step
		OutlierTicks:       270_000,
		Duplicates:         clock.DuplicateDrop,
	}
}

// Representation is the independently recovered state of one bitrate.
type Representation struct {
	ID           int
	cfg          Config
	ec           mediaerr.Collector
	pcrUnwrap    clock.PCRUnwrapper
	drift        clock.DriftEstimator
	timeline     *clock.Timeline
	indexBuilder *index.Builder

	// Anchor handoff. The wall-clock anchor needs both a corrected PCR and a
	// sample PTS; whichever arrives second completes the binding.
	anchorPTS     int64
	haveAnchorPTS bool
	firstPCR27    int64
	haveFirstPCR  bool
	// pending samples awaiting an anchor, bounded by MaxPendingSamples.
	pending []pendingSample

	// Samples retained for segment emission (bounded by the segmenter).
	samples []clock.Sample
	failed  bool
}

// MaxPendingSamples bounds memory for samples held before a PCR anchor.
const MaxPendingSamples = 4096

type pendingSample struct {
	rawPTS, rawDTS int64
	keyframe       bool
	offset         int64
}

// NewRepresentation creates recovery state for bitrate id.
func NewRepresentation(id int, cfg Config) *Representation {
	r := &Representation{
		ID:           id,
		cfg:          cfg,
		drift:        clock.DriftEstimator{MaxCorrectionTicks: cfg.MaxDriftCorrection, OutlierTicks: cfg.OutlierTicks},
		indexBuilder: index.NewBuilder(cfg.KeyframePID),
	}
	r.timeline = clock.NewTimeline(cfg.KeyframePID, clock.TimelineConfig{Duplicates: cfg.Duplicates}, &r.ec)
	return r
}

// Errors returns the structured errors recorded for this representation.
func (r *Representation) Errors() []*mediaerr.Error { return r.ec.All() }

// Failed reports whether a structural, unrecoverable error occurred.
func (r *Representation) Failed() bool { return r.failed || r.ec.HasFatal() }

// ObservePCR unwraps and drift-corrects one PCR observation. A missing first
// anchor is a recoverable condition that flags the representation until
// media timing arrives.
func (r *Representation) ObservePCR(rawPCR int64, localTicks27 int64, offset int64) {
	ticks, wrapped, err := r.pcrUnwrap.Unwrap(rawPCR)
	if err != nil {
		r.record(err, offset)
		return
	}
	if wrapped {
		r.ec.Add(mediaerr.Recoverable(mediaerr.CodePCRWrap, r.cfg.KeyframePID, offset,
			"PCR crossed the 42-bit wrap boundary",
			"expand with accumulated wrap count; continue timeline"))
	}
	obs, err := r.drift.Observe(ticks, localTicks27, &r.ec, r.cfg.KeyframePID, offset)
	if err != nil {
		r.record(err, offset)
		return
	}
	if obs.Outlier {
		return
	}
	if !r.haveFirstPCR {
		r.haveFirstPCR = true
		r.firstPCR27 = obs.CorrectedTicks
	}
	// If a PTS is already waiting, complete the anchor and drain the queue.
	if r.haveAnchorPTS && !r.timeline.Anchored() {
		if err := r.timeline.SetPCRAnchor(r.anchorPTS, r.firstPCR27); err != nil {
			r.record(err, 0)
			return
		}
		r.drainPending()
	}
}

// drainPending processes samples that arrived before the anchor in order.
func (r *Representation) drainPending() {
	queued := r.pending
	r.pending = nil
	for _, p := range queued {
		s, err := r.timeline.AddSample(p.rawPTS, p.rawDTS, p.keyframe, p.offset)
		if err != nil {
			r.record(err, p.offset)
			continue
		}
		r.samples = append(r.samples, *s)
	}
}

func (r *Representation) record(err error, offset int64) {
	if me, ok := err.(*mediaerr.Error); ok {
		me.Offset = offset
		r.ec.Add(me)
		if me.Severity == mediaerr.SeverityFatal {
			r.failed = true
		}
	}
}

// AddSample feeds one PES sample (raw 33-bit PTS/DTS). Samples that arrive
// before the first corrected PCR are queued (bounded) and a locatable
// missing-anchor error is recorded once; once the anchor lands they are
// processed in arrival order.
func (r *Representation) AddSample(rawPTS, rawDTS int64, keyframe bool, offset int64) {
	if r.timeline.Anchored() {
		s, err := r.timeline.AddSample(rawPTS, rawDTS, keyframe, offset)
		if err != nil {
			r.record(err, offset)
			return
		}
		r.samples = append(r.samples, *s)
		return
	}

	if !r.haveAnchorPTS {
		r.haveAnchorPTS = true
		r.anchorPTS = rawPTS
	}
	// A PCR is already available: bind immediately using the first PTS.
	if r.haveFirstPCR {
		if err := r.timeline.SetPCRAnchor(r.anchorPTS, r.firstPCR27); err != nil {
			r.record(err, offset)
			return
		}
		s, err := r.timeline.AddSample(rawPTS, rawDTS, keyframe, offset)
		if err != nil {
			r.record(err, offset)
			return
		}
		r.samples = append(r.samples, *s)
		r.drainPending()
		return
	}

	// No PCR yet: record the condition once and queue.
	if len(r.pending) == 0 {
		r.ec.Add(mediaerr.Recoverable(mediaerr.CodeMissingPCRAnchor, r.cfg.KeyframePID, offset,
			"media sample arrived before any corrected PCR",
			"queue samples until a PCR anchor is established"))
	}
	if len(r.pending) >= MaxPendingSamples {
		r.ec.Add(mediaerr.Recoverable(mediaerr.CodeSampleUnindexable, r.cfg.KeyframePID, offset,
			"pre-anchor sample queue full", "drop oldest queued sample"))
		r.pending = r.pending[1:]
	}
	r.pending = append(r.pending, pendingSample{rawPTS, rawDTS, keyframe, offset})
}

// Index builds the random-access index from the recovered keyframe samples.
// A failed representation yields no index.
func (r *Representation) Index() *index.Index {
	if r.Failed() {
		return index.NewIndex(nil)
	}
	for i, s := range r.samples {
		if !s.Indexable {
			continue
		}
		end := s.WallNanos
		if i+1 < len(r.samples) {
			end = r.samples[i+1].WallNanos
		}
		r.indexBuilder.AddSample(i, s.IsKeyframe, s.Indexable, s.WallNanos, end, s.ByteOffset)
	}
	return index.NewIndex(r.indexBuilder.Build())
}

// Samples returns the recovered samples.
func (r *Representation) Samples() []clock.Sample { return r.samples }

// AnchorMissing reports that no PCR anchor was ever observed.
func (r *Representation) AnchorMissing() bool {
	return !r.drift.Started()
}

// Group coordinates the independent representations of one stream set.
type Group struct {
	reps map[int]*Representation
}

// NewGroup creates an empty fan-out group.
func NewGroup() *Group {
	return &Group{reps: map[int]*Representation{}}
}

// Add registers a representation configuration.
func (g *Group) Add(id int, cfg Config) *Representation {
	r := NewRepresentation(id, cfg)
	g.reps[id] = r
	return r
}

// Get returns the representation with id.
func (g *Group) Get(id int) *Representation { return g.reps[id] }

// All returns the representations keyed by id.
func (g *Group) All() map[int]*Representation { return g.reps }

// Successful returns only representations without structural failure; failed
// ones are excluded from downstream segment results.
func (g *Group) Successful() map[int]*Representation {
	out := map[int]*Representation{}
	for id, r := range g.reps {
		if !r.Failed() {
			out[id] = r
		}
	}
	return out
}

// SegmentsFor builds segment index entries per representation from the
// recovered samples, partitioned by sample count.
func (g *Group) SegmentsFor(samplesPerSegment int) map[int][]*segment.Segment {
	out := map[int][]*segment.Segment{}
	for id, r := range g.Successful() {
		out[id] = buildSegments(id, samplesPerSegment, r)
	}
	return out
}

func buildSegments(repID, perSegment int, r *Representation) []*segment.Segment {
	samples := r.Samples()
	var segs []*segment.Segment
	for start := 0; start < len(samples); start += perSegment {
		end := start + perSegment
		if end > len(samples) {
			end = len(samples)
		}
		chunk := samples[start:end]
		seg := &segment.Segment{
			ID:             len(segs),
			Representation: repID,
			SourceStart:    chunk[0].ByteOffset,
			SourceEnd:      chunk[len(chunk)-1].ByteOffset,
			StartNanos:     chunk[0].WallNanos,
			EndNanos:       chunk[len(chunk)-1].WallNanos,
			SampleCount:    len(chunk),
		}
		for i, s := range chunk {
			if !s.IsKeyframe || !s.Indexable {
				continue
			}
			segEnd := s.WallNanos
			if start+i+1 < len(samples) {
				segEnd = samples[start+i+1].WallNanos
			}
			seg.KeyframeIndex = append(seg.KeyframeIndex, segment.IndexEntry{
				SampleIndex: start + i,
				KeyframePID: r.cfg.KeyframePID,
				StartNanos:  s.WallNanos,
				EndNanos:    segEnd,
				ByteOffset:  s.ByteOffset,
			})
		}
		segs = append(segs, seg)
	}
	return segs
}
