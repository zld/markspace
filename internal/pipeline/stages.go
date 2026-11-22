package pipeline

import (
	"fmt"
	"sort"
	"strings"

	"mediad/internal/clock"
	"mediad/internal/core"
	"mediad/internal/diagnose"
	"mediad/internal/index"
	"mediad/internal/ingest"
	"mediad/internal/validate"
)

// execute runs one stage and stores its output.
func (r *Runner) execute(stage string, assets []*ingest.Asset, out *Output, cache *parseCache) error {
	switch stage {
	case StageIngest:
		return r.runIngest(assets, out)
	case StageNormalize:
		return r.runNormalize(out, cache)
	case StageIndex:
		return r.runIndex(assets, out, cache)
	case StageDiagnose:
		return r.runDiagnose(assets, out)
	case StageValidate:
		return r.runValidate(assets, out)
	}
	return fmt.Errorf("pipeline: unknown stage %q", stage)
}

// parsedAssets keeps the typed parse results between stages without forcing
// them through the checkpoint encoding.
type parseCache struct {
	mp4     map[string]*ingest.MP4File
	subs    map[string]*ingest.Subs
	rtp     map[string]*ingest.RTPStream
	drift   map[string]*ingest.DriftAsset
	tc      map[string]*ingest.TimecodeAsset
	formats map[string]string
}

// ensureCache parses every asset into typed form. Later stages address the
// cache directly, so a resumed run that skipped ingest can still rebuild it
// from the files without committing a new ingest payload.
func (r *Runner) ensureCache(assets []*ingest.Asset, out *Output) (*parseCache, error) {
	if out.cache != nil {
		return out.cache, nil
	}
	cache, st, err := r.parseAssets(assets)
	if err != nil {
		return nil, err
	}
	out.cache = cache
	if out.Ingest == nil {
		out.Ingest = st
	}
	return cache, nil
}

func (r *Runner) runIngest(assets []*ingest.Asset, out *Output) error {
	cache, st, err := r.parseAssets(assets)
	if err != nil {
		return err
	}
	out.Ingest = st
	out.cache = cache
	return nil
}

func (r *Runner) parseAssets(assets []*ingest.Asset) (*parseCache, *IngestStage, error) {
	cache := &parseCache{
		mp4:     make(map[string]*ingest.MP4File),
		subs:    make(map[string]*ingest.Subs),
		rtp:     make(map[string]*ingest.RTPStream),
		drift:   make(map[string]*ingest.DriftAsset),
		tc:      make(map[string]*ingest.TimecodeAsset),
		formats: make(map[string]string, len(assets)),
	}
	st := &IngestStage{}
	for _, a := range assets {
		cache.formats[a.Name] = a.Format
		switch a.Format {
		case "mp4":
			f, err := ingest.ParseMP4(a)
			if err != nil {
				return nil, nil, fmt.Errorf("pipeline: mp4 %s: %w", a.Name, err)
			}
			cache.mp4[a.Name] = f
			st.Issues = append(st.Issues, f.Issues...)
		case "srt", "vtt":
			var subs *ingest.Subs
			var err error
			if a.Format == "srt" {
				subs, err = ingest.ParseSRT(a)
			} else {
				subs, err = ingest.ParseVTT(a)
			}
			if err != nil {
				return nil, nil, fmt.Errorf("pipeline: subtitle %s: %w", a.Name, err)
			}
			cache.subs[a.Name] = subs
			st.Issues = append(st.Issues, subs.Issues...)
			for _, c := range subs.Cues {
				st.Cues = append(st.Cues, CueRecord{
					Asset: a.Name, Format: a.Format, Index: c.Index, ID: c.ID,
					Start: c.Top, End: c.Bottom, Span: c.Span, Lines: c.Lines, Precision: c.Precision,
				})
			}
		case "rtp":
			stream, err := ingest.ParseRTP(a, r.Params.ClockRate)
			if err != nil {
				return nil, nil, fmt.Errorf("pipeline: rtp %s: %w", a.Name, err)
			}
			cache.rtp[a.Name] = stream
			st.Issues = append(st.Issues, stream.IssueList...)
			st.RTP = append(st.RTP, RTPSummary{
				Asset: a.Name, Packets: len(stream.Records), Wraps: stream.Wraps,
				GapLoss: stream.GapLoss, OutOfOrder: stream.OutOfOrder,
			})
		case "drift":
			d, err := ingest.ParseDrift(a)
			if err != nil {
				return nil, nil, fmt.Errorf("pipeline: drift %s: %w", a.Name, err)
			}
			cache.drift[a.Name] = d
			st.Issues = append(st.Issues, d.Issues...)
			if d.Model != nil {
				st.Drifts = append(st.Drifts, DriftRecord{
					Asset: a.Name, Node: d.Model.Node, Samples: d.Model.Samples,
					Slope: d.Model.Slope, Intercept: d.Model.Intercept,
					RatePPB: d.Model.RatePPB, MaxResidual: d.Model.MaxResidual,
					Applied: d.Model.Applied,
					Span:    core.Span{Start: d.Samples[0].Span.Start, End: d.Samples[len(d.Samples)-1].Span.End},
				})
			}
		case "timecode":
			tc, err := ingest.ParseTimecodes(a)
			if err != nil {
				return nil, nil, fmt.Errorf("pipeline: timecode %s: %w", a.Name, err)
			}
			cache.tc[a.Name] = tc
			st.Issues = append(st.Issues, tc.Issues...)
			for _, l := range tc.Labels {
				st.Timecodes = append(st.Timecodes, TimecodeRecord{
					Asset: a.Name, Raw: l.Timecode.Raw, Rate: l.Timecode.Rate,
					Drop: l.Timecode.Drop, Frames: l.Timecode.FramesTotal,
					Clock: l.Timecode.Top, Span: l.Span,
				})
			}
		default:
			// Unclassified assets are hashed and reported, never parsed.
		}
	}
	st.Issues.Sort()
	sort.SliceStable(st.Cues, func(i, j int) bool {
		if st.Cues[i].Start.Cmp(st.Cues[j].Start) != 0 {
			return st.Cues[i].Start.Cmp(st.Cues[j].Start) < 0
		}
		if st.Cues[i].Asset != st.Cues[j].Asset {
			return st.Cues[i].Asset < st.Cues[j].Asset
		}
		return st.Cues[i].Index < st.Cues[j].Index
	})
	return cache, st, nil
}

func (r *Runner) runNormalize(out *Output, cache *parseCache) error {
	b := clock.NewBuilder(r.Params.MasterTrack)
	ns := &NormalizeStage{}
	keys := sortedKeys(cache.formats)
	for _, name := range keys {
		format := cache.formats[name]
		switch format {
		case "mp4":
			f := cache.mp4[name]
			for _, tr := range f.Tracks {
				if tr.Timescale == 0 {
					continue
				}
				name := trackName(name, tr)
				if err := b.AddTrack(clock.Track{
					Name: name, Kind: kindOf(tr.Handler), Asset: nameAsset(name),
					Scale:  core.Rat(1, int64(tr.Timescale)),
					Origin: core.Zero(),
				}); err != nil {
					return fmt.Errorf("pipeline: %w", err)
				}
				for _, s := range tr.Samples {
					if s.Truncated {
						continue
					}
					if _, err := b.AddPoint(name, core.Rat(s.DTS, 1), false, s.Span); err != nil {
						return fmt.Errorf("pipeline: %w", err)
					}
				}
			}
		case "srt", "vtt":
			subs := cache.subs[name]
			track := "subs:" + name
			if err := b.AddTrack(clock.Track{
				Name: track, Kind: clock.KindSubtitle, Asset: name,
				Scale: core.Rat(1, 1000), Origin: core.Zero(),
			}); err != nil {
				return fmt.Errorf("pipeline: %w", err)
			}
			for _, c := range subs.Cues {
				// Cue clocks are seconds; convert to milliseconds for the
				// subtitle track's 1/1000 scale.
				ms := core.Rat(c.Top.Num*1000, c.Top.Den)
				if _, err := b.AddPoint(track, ms, false, c.TimingSpan); err != nil {
					return fmt.Errorf("pipeline: %w", err)
				}
			}
		case "rtp":
			stream := cache.rtp[name]
			track := "rtp:" + name
			model := nodeModel(cache, name)
			// Anchor each capture at its first packet so the RTP clock can be
			// compared with media tracks that start at zero. Raw and extended
			// timestamps are preserved on every point.
			var origin core.Rational
			if len(stream.Records) > 0 {
				// ExtTS is already tick-scaled by the track scale, so the
				// origin must be expressed in seconds, not ticks.
				origin = core.Rat(-stream.Records[0].ExtTS, r.Params.ClockRate)
			}
			tr := clock.Track{
				Name: track, Kind: clock.KindRTP, Asset: name,
				Scale: core.Rat(1, r.Params.ClockRate), Origin: origin,
			}
			if model != nil {
				tr.Drift = model
			}
			if err := b.AddTrack(tr); err != nil {
				return fmt.Errorf("pipeline: %w", err)
			}
			for _, rec := range stream.Records {
				if _, err := b.AddPoint(track, core.Rat(rec.ExtTS, 1), rec.Wrapped, rec.Span); err != nil {
					return fmt.Errorf("pipeline: %w", err)
				}
				if rec.SeqJump > 0 {
					// Lost packets break continuity: a node retry must reset
					// alignment here rather than interpolate across the hole.
					ns.Issues.Add(diagnose.Issue{
						Category: diagnose.CatClockAlign, Stage: diagnose.StageNormalize,
						Format: "rtp", Asset: name, Span: rec.Span,
						Detail:      fmt.Sprintf("alignment gap after %d lost RTP packets", rec.SeqJump-1),
						Recoverable: true,
					})
				}
			}
		case "timecode":
			tc := cache.tc[name]
			track := "tc:" + name
			if err := b.AddTrack(clock.Track{
				Name: track, Kind: clock.KindTimecode, Asset: name,
				Scale: core.Rat(1, 1000), Origin: core.Zero(),
			}); err != nil {
				return fmt.Errorf("pipeline: %w", err)
			}
			for _, l := range tc.Labels {
				ms := core.Rat(l.Timecode.Top.Num*1000, l.Timecode.Top.Den)
				if _, err := b.AddPoint(track, ms, false, l.Span); err != nil {
					return fmt.Errorf("pipeline: %w", err)
				}
			}
		}
	}
	tl, err := b.Seal()
	if err != nil {
		var reg *clock.RegressionError
		if asRegression(err, &reg) {
			ns.Issues.Add(diagnose.Issue{
				Category: diagnose.CatMonotonic, Stage: diagnose.StageNormalize,
				Format: "clock", Asset: reg.Track,
				Span:   reg.Span,
				Detail: reg.Error(),
			})
			// Re-seal without the offending adjustment so the run still yields
			// a usable timeline; the regression is already recorded.
			return fmt.Errorf("pipeline: %w", err)
		}
		return fmt.Errorf("pipeline: %w", err)
	}
	resolveMaster(tl, r.Params.MasterTrack)
	if err := tl.Validate(); err != nil {
		ns.Issues.Add(diagnose.Issue{
			Category: diagnose.CatMonotonic, Stage: diagnose.StageNormalize,
			Format: "clock", Asset: r.Params.MasterTrack,
			Span: core.Span{Start: 0, End: 0}, Detail: err.Error(),
		})
	}
	ns.Tracks = tl.Tracks
	ns.Points = tl.Points
	ns.Wraps = tl.Wraps
	ns.Clamps = tl.Clamps
	ns.Issues.Sort()
	out.Normalize = ns
	return nil
}

// resolveMaster picks the timeline reference. The configured name may be a
// bare kind ("video") while registered tracks are asset-qualified, so the
// first matching track of that kind becomes the master deterministically.
func resolveMaster(tl *clock.Timeline, want string) {
	if want != "" && tl.TrackByName(want) != nil {
		tl.Master = want
		return
	}
	for _, tr := range tl.Tracks {
		if want != "" && (string(tr.Kind) == want || strings.HasPrefix(tr.Name, want+"/")) {
			tl.Master = tr.Name
			return
		}
	}
	for _, tr := range tl.Tracks {
		if tr.Kind == clock.KindVideo {
			tl.Master = tr.Name
			return
		}
	}
	if len(tl.Tracks) > 0 {
		tl.Master = tl.Tracks[0].Name
	}
}

// nodeModel binds a drift sidecar to the capture it belongs to. The match is
// by node name appearing in the capture's path; a capture with no matching
// sidecar is left uncorrected rather than inheriting another node's fit.
func nodeModel(cache *parseCache, rtpName string) *clock.DriftModel {
	var names []string
	for n := range cache.drift {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		d := cache.drift[n]
		if d.Model == nil || d.Node == "" || d.Node == "unknown" {
			continue
		}
		if strings.Contains(rtpName, d.Node) {
			return d.Model
		}
	}
	return nil
}

func kindOf(handler string) clock.TrackKind {
	switch strings.TrimSpace(handler) {
	case "vide":
		return clock.KindVideo
	case "soun":
		return clock.KindAudio
	case "text", "sbtl":
		return clock.KindSubtitle
	}
	return clock.KindVideo
}

func trackName(asset string, tr ingest.MP4Track) string {
	return fmt.Sprintf("%s#%d", asset, tr.TrackID)
}

func nameAsset(track string) string {
	if i := strings.LastIndexByte(track, '#'); i > 0 {
		return track[:i]
	}
	return track
}

func stemOf(name string) string {
	base := name
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	if i := strings.LastIndexByte(base, '.'); i > 0 {
		base = base[:i]
	}
	return base
}

func asRegression(err error, target **clock.RegressionError) bool {
	for err != nil {
		if r, ok := err.(*clock.RegressionError); ok {
			*target = r
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (r *Runner) runIndex(assets []*ingest.Asset, out *Output, cache *parseCache) error {
	st := &IndexStage{}
	damage := damageSpans(out)
	for _, a := range assets {
		f, ok := cache.mp4[a.Name]
		if !ok {
			continue
		}
		spans := damage[a.Name]
		for _, tr := range f.Tracks {
			idx, issues := index.Build(a.Name, tr, spans)
			st.Indexes = append(st.Indexes, *idx)
			st.Issues = append(st.Issues, issues...)
			if err := idx.Validate(a.Size()); err != nil {
				st.Issues.Add(diagnose.Issue{
					Category: diagnose.CatIndexOutOfRange, Stage: diagnose.StageIndex,
					Format: "mp4", Asset: a.Name, Span: idx.Keyframes[0].Span,
					Detail: err.Error(),
				})
			}
		}
	}
	sort.SliceStable(st.Indexes, func(i, j int) bool {
		if st.Indexes[i].Asset != st.Indexes[j].Asset {
			return st.Indexes[i].Asset < st.Indexes[j].Asset
		}
		return st.Indexes[i].TrackID < st.Indexes[j].TrackID
	})
	st.Issues.Sort()
	out.Index = st
	return nil
}

// damageSpans returns the byte ranges already known to be damaged per asset.
// The index stage uses them to mark keyframes whose gaps cannot be trusted.
func damageSpans(out *Output) map[string][]core.Span {
	m := make(map[string][]core.Span)
	if out.Ingest == nil {
		return m
	}
	for _, is := range out.Ingest.Issues {
		if !is.IsDamage() {
			continue
		}
		m[is.Asset] = append(m[is.Asset], is.Span)
	}
	for name := range m {
		sort.SliceStable(m[name], func(i, j int) bool {
			if m[name][i].Start != m[name][j].Start {
				return m[name][i].Start < m[name][j].Start
			}
			return m[name][i].End < m[name][j].End
		})
	}
	return m
}

func (r *Runner) runDiagnose(assets []*ingest.Asset, out *Output) error {
	st := &DiagnoseStage{}
	var all diagnose.Issues
	if out.Ingest != nil {
		all = append(all, out.Ingest.Issues...)
	}
	if out.Normalize != nil {
		all = append(all, out.Normalize.Issues...)
	}
	if out.Index != nil {
		all = append(all, out.Index.Issues...)
	}
	if out.Normalize != nil {
		r.recordClockDamage(out, &all)
	}
	all.Sort()
	st.Issues = all
	st.Ranges = diagnose.Ranges(all)
	sizes := make(map[string]int64, len(assets))
	for _, a := range assets {
		sizes[a.Name] = a.Size()
	}
	if err := diagnose.Validate(st.Ranges, sizes); err != nil {
		return fmt.Errorf("pipeline: corrupt range validation: %w", err)
	}
	out.Diagnose = st
	return nil
}

// recordClockDamage turns observed non-monotonicity into an offset-carrying
// issue. Drift and wrap events are already emitted by their parsers.
func (r *Runner) recordClockDamage(out *Output, all *diagnose.Issues) {
	ns := out.Normalize
	if ns == nil {
		return
	}
	seen := make(map[string]bool)
	for _, p := range ns.Points {
		if !p.Clamped {
			continue
		}
		key := fmt.Sprintf("%s:%d", p.Track, p.Span.Start)
		if seen[key] {
			continue
		}
		seen[key] = true
		asset := nameAsset(p.Track)
		all.Add(diagnose.Issue{
			Category: diagnose.CatMonotonic, Stage: diagnose.StageDiagnose,
			Format: "clock", Asset: asset, Span: p.Span,
			Detail: fmt.Sprintf("clock for %s clamped at %s", p.Track, p.Master),
		})
	}
}

func (r *Runner) runValidate(assets []*ingest.Asset, out *Output) error {
	sizes := make(map[string]int64, len(assets))
	formats := make(map[string]string, len(assets))
	for _, a := range assets {
		sizes[a.Name] = a.Size()
		formats[a.Name] = a.Format
	}
	res := ValidateOutput(out, sizes, formats)
	vs := &ValidateStage{Result: *res}
	// Per-stage gates are derived from each stage payload rather than from
	// in-memory state, so a resumed run that reuses committed stages produces
	// the same gates as a full run.
	for _, g := range r.stageGates(assets, out) {
		vs.Gates = append(vs.Gates, g)
		if g.Failed > 0 {
			vs.Issues.Add(diagnose.Issue{
				Category: diagnose.CatInvariant, Stage: diagnose.StageValidate,
				Format: "report", Asset: "",
				Span:   core.Span{Start: 0, End: 0},
				Detail: fmt.Sprintf("%s stage gate failed %d of %d self-checks", g.Stage, g.Failed, g.Passed+g.Failed),
			})
		}
	}
	sort.SliceStable(vs.Gates, func(i, j int) bool { return vs.Gates[i].Stage < vs.Gates[j].Stage })
	for _, c := range res.FailedChecks() {
		vs.Issues.Add(diagnose.Issue{
			Category: diagnose.CatInvariant, Stage: diagnose.StageValidate,
			Format: "report", Asset: c.Asset, Span: c.Span,
			Detail: fmt.Sprintf("%s: %s", c.Name, c.Detail),
		})
	}
	vs.Issues.Sort()
	out.Validate = vs
	return nil
}

// stageGates validates each stage's published output in execution order.
func (r *Runner) stageGates(assets []*ingest.Asset, out *Output) []StageCheck {
	sizes := make(map[string]int64, len(assets))
	for _, a := range assets {
		sizes[a.Name] = a.Size()
	}
	var gates []StageCheck
	if out.Ingest != nil {
		cues := make([]validate.CueRef, 0, len(out.Ingest.Cues))
		for _, c := range out.Ingest.Cues {
			cues = append(cues, validate.CueRef{Asset: c.Asset, Index: c.Index, Span: c.Span, TimingSpan: c.Span})
		}
		gates = append(gates, toGate("ingest", validate.IngestStage(sizes, out.Ingest.Issues, cues)))
	}
	if out.Normalize != nil {
		tl, err := rebuildTimeline(out.Normalize)
		if err != nil {
			tl = nil
		}
		gates = append(gates, toGate("normalize", validate.NormalizeStage(tl, out.Normalize.Points)))
	}
	if out.Index != nil {
		idxs := make([]*index.Index, 0, len(out.Index.Indexes))
		for i := range out.Index.Indexes {
			idxs = append(idxs, &out.Index.Indexes[i])
		}
		gates = append(gates, toGate("index", validate.IndexStage(keyframeIndexRefs(idxs))))
	}
	if out.Diagnose != nil {
		refs := make([]validate.RangeRef, 0, len(out.Diagnose.Ranges))
		for _, rg := range out.Diagnose.Ranges {
			refs = append(refs, validate.RangeRef{Asset: rg.Asset, Format: rg.Format, Span: rg.Span})
		}
		gates = append(gates, toGate("diagnose", validate.DiagnoseStage(refs, sizes)))
	}
	return gates
}

func toGate(stage string, res *validate.Result) StageCheck {
	return StageCheck{Stage: stage, Name: stage + "_gate", Passed: res.Total - res.Failed, Failed: res.Failed}
}
