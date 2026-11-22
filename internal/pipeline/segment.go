// Package pipeline orchestrates segmentation: it maps tracks onto the unified
// clock, aligns cuts to sync frames, and content-addresses every slice.
package pipeline

import (
	"errors"
	"fmt"
	"sort"

	"github.com/pairwise/mediad/internal/audio"
	"github.com/pairwise/mediad/internal/clock"
	"github.com/pairwise/mediad/internal/fault"
	"github.com/pairwise/mediad/internal/fingerprint"
	"github.com/pairwise/mediad/internal/index"
	"github.com/pairwise/mediad/internal/mp4"
)

// MaxSegmentNS caps a single delivered segment at 30 minutes.
const MaxSegmentNS int64 = 30 * 60 * 1_000_000_000

// SampleSource exposes verified sample payloads to the hasher.
type SampleSource struct {
	tree *mp4.Tree
	byID map[uint32]*mp4.Track
}

// NewSampleSource indexes movie tracks for payload reads.
func NewSampleSource(tree *mp4.Tree, movie *mp4.Movie) *SampleSource {
	by := make(map[uint32]*mp4.Track, len(movie.Tracks))
	for _, t := range movie.Tracks {
		by[t.ID] = t
	}
	return &SampleSource{tree: tree, byID: by}
}

// Count implements fingerprint.Samples.
func (s *SampleSource) Count(trackID uint32) int {
	if t := s.byID[trackID]; t != nil {
		return len(t.Samples)
	}
	return 0
}

// ReadSample reads one mdat-validated sample payload.
func (s *SampleSource) ReadSample(trackID uint32, i int) ([]byte, error) {
	t := s.byID[trackID]
	if t == nil || i < 0 || i >= len(t.Samples) {
		return nil, fault.Errorf("pipeline.sample", fault.KindParse, "sample %d outside track %d", i, trackID).WithTrack(trackID)
	}
	smp := t.Samples[i]
	if !smp.InMdat {
		return nil, fault.Errorf("pipeline.sample", fault.KindParse, "sample %d of track %d was never validated", smp.Index, trackID).
			WithTrack(trackID).WithSample(smp.Index)
	}
	return s.tree.ReadAt(smp.Offset, int64(smp.Size))
}

// Segment is one deliverable slice.
type Segment struct {
	ID           string
	TrackID      uint32
	StartNS      int64
	EndNS        int64
	FirstSample  int
	SampleCount  int
	KeyOffset    int64
	KeyPresented int64
	KeyExact     bool
	Content      [32]byte
	ContentHex   string
	SHA256       string
	SilentNS     int64
	SilenceKnown bool
	Recovered    bool
}

// Request asks for one track range.
type Request struct {
	TrackID   uint32
	StartNS   int64
	EndNS     int64
	MaxSpanNS int64
}

// Result is the output of a segmentation run.
type Result struct {
	Segments     []Segment
	Silence      map[uint32]audio.Result
	KeyframeHits int
	Cuts         int
}

// Presentation caches per-track presentation times computed once, in order,
// because TrackClock is a stateful monotonicity checker.
type Presentation struct {
	times  map[uint32][]int64
	usable map[uint32][]bool
	clocks map[uint32]*clock.TrackClock
}

// ComputePresentation converts every sample of every track exactly once.
// Samples whose timestamps fail the clock checks are marked unusable.
func ComputePresentation(movie *mp4.Movie, clocks map[uint32]*clock.TrackClock) *Presentation {
	p := &Presentation{
		times:  map[uint32][]int64{},
		usable: map[uint32][]bool{},
		clocks: clocks,
	}
	for _, tr := range movie.Tracks {
		clk := clocks[tr.ID]
		n := len(tr.Samples)
		times := make([]int64, n)
		usable := make([]bool, n)
		if clk != nil {
			// Stop at the first rejected timestamp: once the clock is proven
			// unreliable, later readings cannot be distinguished from
			// post-wrap garbage, so they are withheld rather than guessed.
			for i, s := range tr.Samples {
				v, err := clk.Presentation(s.DTS, s.CTO)
				if err != nil {
					break
				}
				times[i], usable[i] = v, true
			}
		}
		p.times[tr.ID], p.usable[tr.ID] = times, usable
	}
	return p
}

// At returns the presentation time of a sample when known.
func (p *Presentation) At(trackID uint32, i int) (int64, bool) {
	if p == nil {
		return 0, false
	}
	ts := p.times[trackID]
	if i < 0 || i >= len(ts) || !p.usable[trackID][i] {
		return 0, false
	}
	return ts[i], true
}

// Times exposes the raw slice (zero for unusable samples).
func (p *Presentation) Times(trackID uint32) []int64 { return p.times[trackID] }

// SegmentPlan is a cut that has been aligned but not yet hashed.
type SegmentPlan struct {
	TrackID      uint32
	StartNS      int64
	EndNS        int64
	FirstSample  int
	SampleCount  int
	KeyOffset    int64
	KeyPresented int64
	KeyExact     bool
	Gap          int64
	Recovered    bool
}

// Planner wires parsed media to the index and fingerprint stages.
type Planner struct {
	Movie   *mp4.Movie
	Index   *index.Index
	Pres    *Presentation
	Silence map[uint32]audio.Result
	Samples *SampleSource
	// Recovered marks the whole run as salvaged from damaged input.
	Recovered bool
}

// NewPlanner builds a planner from parsed media and cached presentations.
func NewPlanner(movie *mp4.Movie, tree *mp4.Tree, ix *index.Index, pres *Presentation, silence map[uint32]audio.Result) (*Planner, error) {
	if movie == nil || tree == nil {
		return nil, fault.Errorf("pipeline.plan", fault.KindParse, "nil movie or tree")
	}
	if ix == nil {
		return nil, fault.Errorf("pipeline.plan", fault.KindIndex, "nil index").Fatal()
	}
	if silence == nil {
		silence = map[uint32]audio.Result{}
	}
	return &Planner{Movie: movie, Index: ix, Pres: pres, Silence: silence,
		Samples: NewSampleSource(tree, movie)}, nil
}

// Plan aligns each requested range to sync frames without reading payloads.
// Partial failures are returned alongside the plans that succeeded.
func (p *Planner) Plan(reqs []Request) ([]SegmentPlan, error) {
	var out []SegmentPlan
	var problems []error
	for _, req := range reqs {
		tr := p.trackByID(req.TrackID)
		if tr == nil {
			problems = append(problems, fault.Errorf("pipeline.track", fault.KindParse,
				"unknown track %d", req.TrackID).WithTrack(req.TrackID))
			continue
		}
		ti := p.indexFor(req.TrackID)
		if ti == nil {
			problems = append(problems, fault.Errorf("pipeline.track", fault.KindIndex,
				"no keyframe index for track %d", req.TrackID).WithTrack(req.TrackID).Wrapped(fault.ErrNoKeyframe))
			continue
		}
		if req.EndNS <= req.StartNS {
			problems = append(problems, fault.Errorf("pipeline.track", fault.KindIndex,
				"empty range [%d,%d)", req.StartNS, req.EndNS).WithTrack(req.TrackID))
			continue
		}
		maxSpan := req.MaxSpanNS
		if maxSpan <= 0 || maxSpan > MaxSegmentNS {
			maxSpan = MaxSegmentNS
		}
		for cur := req.StartNS; cur < req.EndNS; {
			end := cur + maxSpan
			if end > req.EndNS {
				end = req.EndNS
			}
			plan, err := p.planOne(ti, tr, cur, end)
			if err != nil {
				problems = append(problems, err)
				break
			}
			out = append(out, plan)
			cur = end
		}
	}
	if len(problems) > 0 {
		return out, fault.New("pipeline.plan", fault.KindTruncated, errors.Join(problems...)).Recovered()
	}
	return out, nil
}

func (p *Planner) planOne(ti *index.TrackIndex, tr *mp4.Track, start, end int64) (SegmentPlan, error) {
	entry, gap, err := ti.Align(start)
	if err != nil {
		return SegmentPlan{}, err
	}
	last := p.lastSampleAtOrBefore(tr, end-1)
	if last < entry.Sample {
		last = entry.Sample
	}
	return SegmentPlan{
		TrackID: tr.ID, StartNS: start, EndNS: end, FirstSample: entry.Sample,
		SampleCount: last - entry.Sample + 1, KeyOffset: entry.Offset,
		KeyPresented: entry.Presented, KeyExact: entry.Exact, Gap: gap,
		// A cut whose anchor is inferred, or which had to fall back to an
		// earlier sync frame, is a salvaged slice rather than a clean one.
		Recovered: p.Recovered || !entry.Exact || gap > 0,
	}, nil
}

// Materialize hashes one planned segment into a deliverable slice.
func (p *Planner) Materialize(plan SegmentPlan) (Segment, error) {
	digest, _, err := fingerprint.Sum(p.Samples, plan.TrackID, plan.FirstSample, plan.SampleCount)
	if err != nil {
		return Segment{}, err
	}
	hex := fmt.Sprintf("%x", digest)
	seg := Segment{
		ID: segmentID(plan.TrackID, plan.FirstSample, plan.SampleCount, plan.StartNS), TrackID: plan.TrackID,
		StartNS: plan.StartNS, EndNS: plan.EndNS, FirstSample: plan.FirstSample,
		SampleCount: plan.SampleCount, KeyOffset: plan.KeyOffset, KeyPresented: plan.KeyPresented,
		KeyExact: plan.KeyExact, Content: digest, ContentHex: hex, SHA256: hex,
		Recovered: plan.Recovered,
	}
	p.annotateSilence(&seg)
	return seg, nil
}

// Run plans and materializes in one pass.
func (p *Planner) Run(reqs []Request) (*Result, error) {
	plans, planErr := p.Plan(reqs)
	res := &Result{Silence: p.Silence}
	var problems []error
	if planErr != nil {
		problems = append(problems, planErr)
	}
	for _, plan := range plans {
		seg, err := p.Materialize(plan)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		res.Segments = append(res.Segments, seg)
		res.Cuts++
		if plan.Gap == 0 {
			res.KeyframeHits++
		}
	}
	if len(res.Segments) == 0 {
		if len(problems) > 0 {
			return res, problems[0]
		}
		return res, fault.Errorf("pipeline.run", fault.KindIndex, "no segments produced").Fatal()
	}
	if len(problems) > 0 {
		return res, fault.New("pipeline.run", fault.KindTruncated, errors.Join(problems...)).Recovered()
	}
	return res, nil
}

// annotateSilence measures how much of a slice is covered by detected silence.
// Silence is a property of the presentation timeline, so every analysable
// audio track contributes, not only a track sharing the segment's ID. When no
// audio track could be analysed the slice stays explicitly unknown: an absent
// or undecodable track must never look like a silent one.
func (p *Planner) annotateSilence(seg *Segment) {
	known := false
	var covered []audio.Range
	for _, res := range p.Silence {
		if !res.Detectable {
			continue
		}
		known = true
		for _, r := range res.Silence {
			if hi, lo := min64(seg.EndNS, r.End), max64(seg.StartNS, r.Start); hi > lo {
				covered = append(covered, audio.Range{Start: max64(seg.StartNS, r.Start), End: hi})
			}
		}
	}
	if !known {
		return
	}
	seg.SilenceKnown = true
	// Merge overlapping intervals from multiple audio tracks so a shared
	// minute of silence is not counted twice.
	sort.Slice(covered, func(i, j int) bool { return covered[i].Start < covered[j].Start })
	var cur *audio.Range
	for _, r := range covered {
		if cur == nil || r.Start > cur.End {
			if cur != nil {
				seg.SilentNS += cur.End - cur.Start
			}
			c := r
			cur = &c
			continue
		}
		if r.End > cur.End {
			cur.End = r.End
		}
	}
	if cur != nil {
		seg.SilentNS += cur.End - cur.Start
	}
}

func (p *Planner) trackByID(id uint32) *mp4.Track {
	for _, t := range p.Movie.Tracks {
		if t.ID == id {
			return t
		}
	}
	return nil
}

func (p *Planner) indexFor(id uint32) *index.TrackIndex {
	for _, ti := range p.Index.Tracks {
		if ti.TrackID == id {
			return ti
		}
	}
	return nil
}

// lastSampleAtOrBefore returns the greatest usable sample index presented at
// or before t. When no presentation data exists, the final verified sample is
// used, which errs toward including all readable content.
func (p *Planner) lastSampleAtOrBefore(tr *mp4.Track, t int64) int {
	n := len(tr.Samples)
	if n == 0 {
		return -1
	}
	any := false
	best := 0
	for i := 0; i < n; i++ {
		pt, ok := p.Pres.At(tr.ID, i)
		if !ok {
			continue
		}
		any = true
		if pt <= t {
			best = i
		}
	}
	if !any {
		return n - 1
	}
	return best
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
