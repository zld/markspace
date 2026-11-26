// Package pipeline wires manifest parsing, demuxing, track mapping, remuxing,
// keyframe indexing and verification into one reproducible run.
package pipeline

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"liveregress/internal/clock"
	"liveregress/internal/fault"
	"liveregress/internal/fixture"
	"liveregress/internal/fmp4"
	"liveregress/internal/index"
	"liveregress/internal/manifest"
	"liveregress/internal/recover"
)

// Options configures one regression run.
type Options struct {
	Root            string
	Case            string
	OutDir          string
	Recover         bool
	RebuildIndex    bool
	MaxSegmentBytes int64
}

// Runner executes one regression pass.
type Runner struct {
	opts    Options
	report  *Report
	cp      *recover.Checkpoint
	outRoot string
}

// Run executes the pipeline and returns a report. Non-nil error means the run
// could not start (bad root); media defects are recorded in the report.
func Run(opts Options) (*Report, error) {
	absRoot, err := filepath.Abs(opts.Root)
	if err != nil {
		return nil, fault.Wrap(fault.KindPath, opts.Root, -1, "resolve root", err)
	}
	st, err := os.Stat(absRoot)
	if err != nil || !st.IsDir() {
		return nil, fault.New(fault.KindPath, opts.Root, -1, "input root is not a directory")
	}
	if opts.MaxSegmentBytes <= 0 {
		opts.MaxSegmentBytes = 64 << 20
	}
	r := &Runner{opts: opts, report: &Report{Case: opts.Case, Root: absRoot}}
	r.outRoot = opts.OutDir
	if r.outRoot == "" {
		r.outRoot = filepath.Join(absRoot, ".out")
	}
	if err := os.MkdirAll(r.outRoot, 0o755); err != nil {
		return nil, fault.Wrap(fault.KindPath, r.outRoot, -1, "create output dir", err)
	}
	r.report.OutRoot = r.outRoot
	r.cp = recover.New(opts.Case+":"+filepath.Base(absRoot), absRoot, opts.Case)
	if opts.Recover {
		if loaded, lerr := recover.Load(r.outRoot); lerr != nil {
			r.report.AddFault(lerr)
		} else if loaded != nil {
			r.cp = loaded
		}
	}
	r.run()
	r.report.Summary()
	if err := r.cp.Save(r.outRoot); err != nil {
		r.report.AddFault(err)
	}
	return r.report, nil
}

func (r *Runner) run() {
	master, err := manifest.LoadMaster(r.opts.Root, fixture.MasterName)
	if err != nil {
		r.report.AddFault(err)
		return
	}
	// Video variants are ordered by bandwidth for a stable report.
	variants := append([]manifest.Variant(nil), master.Variants...)
	sort.SliceStable(variants, func(i, j int) bool {
		return layerOf(variants[i].Path) < layerOf(variants[j].Path)
	})
	for _, v := range variants {
		r.runLayer(layerOf(v.Path), v.Path, "video")
	}
	var audio []manifest.Rendition
	for _, rn := range master.Renditions {
		if rn.Type == "AUDIO" && rn.Path != "" {
			audio = append(audio, rn)
		}
	}
	sort.SliceStable(audio, func(i, j int) bool { return layerOf(audio[i].Path) < layerOf(audio[j].Path) })
	for _, rn := range audio {
		r.runLayer(layerOf(rn.Path), rn.Path, "audio")
	}
}

func layerOf(childPath string) string {
	p := strings.TrimSuffix(childPath, "/"+fixture.MediaName)
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// runLayer processes one representation. A non-recoverable defect isolates the
// layer; segments attempted before it remain in the report.
func (r *Runner) runLayer(layer, playlistPath, kind string) {
	lr := LayerReport{Layer: layer, Kind: kind, Playlist: playlistPath}
	pl, err := manifest.LoadMedia(r.opts.Root, playlistPath)
	if err != nil {
		r.report.AddFault(err)
		r.isolate(&lr, err)
		r.report.Layers = append(r.report.Layers, lr)
		return
	}
	if checkRoundtrip(pl, 90000) {
		r.report.ParseRoundtrip++
	} else {
		r.report.AddFault(fault.New(fault.KindCorrupt, playlistPath, -1,
			"declared timeline does not round-trip at 90 kHz"))
	}
	r.report.ParseTotal++

	var perSegment [][]sampleFact
	var gapFaults []*fault.Error
	for _, seg := range pl.Segments {
		out := r.processSegment(layer, kind, seg)
		out.report.Sequence = int64(len(lr.Segments))
		lr.Segments = append(lr.Segments, out.report)
		for _, f := range out.report.Faults {
			r.report.AddFault(f)
		}
		perSegment = append(perSegment, out.samples)
		if out.isolate {
			r.isolate(&lr, out.layerFault)
			// Samples before the stop still carry real keyframes, so index
			// verification must consider only committed segments.
			break
		}
		if gf := r.checkContinuity(layer, seg, out); gf != nil {
			gapFaults = append(gapFaults, gf)
			r.report.AddFault(gf)
		}
	}
	timescale := uint32(90000)
	if ts := r.layerTimescale(pl); ts != 0 {
		timescale = ts
	}
	// Restrict keyframe evidence to segments actually committed; an isolated
	// layer's unread tail is not evidence of a missing keyframe.
	var committedFacts []keyframeFact
	for i := range perSegment {
		if i < len(lr.Segments) && !lr.Segments[i].Complete {
			continue
		}
		committedFacts = append(committedFacts, factsForSegment(perSegment[i])...)
	}
	r.verifyKeyframes(&lr, pl, kind, perSegment, committedFacts, timescale)
	lr.Gaps = gapFaults
	r.report.Layers = append(r.report.Layers, lr)
}

// factsForSegment returns the keyframes observed in one segment's samples.
func factsForSegment(samples []sampleFact) []keyframeFact {
	var out []keyframeFact
	for _, s := range samples {
		if !s.Keyframe {
			continue
		}
		out = append(out, keyframeFact{Time: s.Time, Offset: s.Offset,
			Size: s.Size, TrackID: s.TrackID})
	}
	return out
}

func checkRoundtrip(pl *manifest.Playlist, timescale uint32) bool {
	bounds := pl.Boundaries(timescale)
	if len(bounds) != len(pl.Segments)+1 {
		return false
	}
	// Each boundary equals the independently converted segment start.
	var acc clock.Rational
	for i, seg := range pl.Segments {
		if bounds[i].Ticks != acc.Ticks(timescale) {
			return false
		}
		acc = acc.Add(seg.Duration)
	}
	return bounds[len(bounds)-1].Ticks == acc.Ticks(timescale)
}

func (r *Runner) isolate(lr *LayerReport, err error) {
	lr.Isolated = true
	reason := "unknown"
	var offset int64
	if fe, ok := fault.As(err); ok {
		reason = string(fe.Kind)
		offset = fe.Offset
	}
	lr.IsolatedReason = reason
	lr.IsolatedOffset = offset
	r.cp.Isolate(recover.LayerState{Layer: lr.Layer, Isolated: true, Reason: reason, Offset: offset})
}

// checkContinuity compares the playlist's declared segment start against the
// media's own tfdt and against the running declared timeline.
func (r *Runner) checkContinuity(layer string, seg manifest.Segment, out segmentOutcome) *fault.Error {
	if len(out.keyframes) == 0 && out.report.Samples == 0 {
		return nil
	}
	media, err := r.mediaTime(seg)
	if err != nil || !media.Valid() {
		return nil
	}
	declared := clock.Time{Ticks: seg.Start.Ticks(media.Timescale), Timescale: media.Timescale}
	if declared.Ticks == media.Ticks {
		return nil
	}
	deviation := media.Ticks - declared.Ticks
	fe := fault.New(fault.KindGap, seg.Path, -1,
		fmt.Sprintf("declared start %d ticks, media starts at %d ticks", declared.Ticks, media.Ticks)).
		InLayer(layer).AtTime(declared).Recover(true)
	r.report.Gaps = append(r.report.Gaps, Gap{
		Layer: layer, AtSequence: seg.Sequence,
		Declared: declared.String(), Actual: media.String(),
		DeviationTicks: deviation, Timescale: media.Timescale,
	})
	return fe
}

// mediaTime returns the first fragment's base decode time for a segment.
func (r *Runner) mediaTime(seg manifest.Segment) (clock.Time, error) {
	data, err := os.ReadFile(filepath.Join(r.opts.Root, filepath.FromSlash(seg.Path)))
	if err != nil {
		return clock.Time{}, err
	}
	parsed, perr := fmp4.ParseSegmentBytes(data, seg.Path)
	if parsed == nil || len(parsed.Fragments) == 0 {
		return clock.Time{}, perr
	}
	initBytes, err := r.loadInit(seg)
	if err != nil {
		return clock.Time{}, err
	}
	init, err := fmp4.ParseInitBytes(initBytes, seg.Path+"#init")
	if err != nil || len(init.Tracks) != 1 {
		return clock.Time{}, fmt.Errorf("init unavailable")
	}
	ts := init.Tracks[0].Timescale
	if ts == 0 {
		ts = 90000
	}
	return clock.Time{Ticks: int64(parsed.Fragments[0].BaseDecodeTime), Timescale: ts}, nil
}

func (r *Runner) layerTimescale(pl *manifest.Playlist) uint32 {
	if len(pl.Segments) == 0 {
		return 0
	}
	t, err := r.mediaTime(pl.Segments[0])
	if err != nil {
		return 0
	}
	return t.Timescale
}

// verifyKeyframes checks the declared I-frame playlist (or rebuilds one) against
// keyframes actually present in the media.
func (r *Runner) verifyKeyframes(lr *LayerReport, pl *manifest.Playlist, kind string, perSegment [][]sampleFact, facts []keyframeFact, timescale uint32) {
	if kind != "video" {
		return
	}
	observed := make([]index.Keyframe, 0, len(facts))
	for _, f := range facts {
		observed = append(observed, index.Keyframe{Time: f.Time, Offset: f.Offset,
			Size: f.Size, TrackID: f.TrackID})
	}
	if r.opts.RebuildIndex {
		rebuilt, err := r.rebuildIndex(lr, pl, perSegment, observed, timescale)
		if err != nil {
			r.report.AddFault(err)
			lr.Isolated = true
			lr.IsolatedReason = "index"
			return
		}
		lr.Rebuilt = true
		hits, _, _ := index.Verify(rebuilt, observed)
		lr.KeyframeHits, lr.KeyframeTotal = hits, len(observed)
		return
	}
	declared, err := r.loadIFrameIndex(lr, pl, timescale)
	if err != nil {
		r.report.AddFault(err)
		lr.Isolated = true
		lr.IsolatedReason = "index"
		return
	}
	if declared == nil {
		r.report.AddFault(fault.New(fault.KindIndex, pl.Path, -1,
			"I-frame index is missing; rerun with --rebuild-index").Recover(true))
		lr.Isolated = true
		lr.IsolatedReason = "missing index"
		return
	}
	hits, fp, missed := index.Verify(declared, observed)
	lr.KeyframeHits, lr.KeyframeTotal = hits, len(observed)
	lr.IndexFalsePositives = fp
	if fp > 0 || missed > 0 {
		fe := fault.New(fault.KindIndex, declared.Path, 0,
			fmt.Sprintf("I-frame index disagrees with media: hits=%d false=%d missed=%d", hits, fp, missed)).
			Recover(hits > 0)
		r.report.AddFault(fe)
		if hits == 0 {
			lr.Isolated = true
			lr.IsolatedReason = "index"
		}
	}
}

// Report and layer types live in report.go.
