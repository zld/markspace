// Package timeline merges subtitle, timecode and video tracks into one
// deterministically ordered index on the shared media clock.
package timeline

import (
	"sort"

	"mediaaudit/internal/model"
)

// ItemKind describes what an index entry refers to.
type ItemKind string

const (
	ItemVideo    ItemKind = "video_sample"
	ItemSubtitle ItemKind = "subtitle_piece"
	ItemTimecode ItemKind = "timecode_mark"
)

// Item is one entry on the unified timeline. Key is a stable identity string
// that does not depend on input order; Seq is assigned after sorting and is
// what the checkpoint layer hashes.
type Item struct {
	Seq       int
	Track     model.Track
	Kind      ItemKind
	Key       string
	Span      model.Span
	Source    string // originating file or track label
	SourceIdx int    // index within the source
	Payload   string // cue text or sample label, kept for frame rendering
}

// Boundary describes a cut point contributed by any track.
type Boundary struct {
	Time  model.Rat
	Track model.Track
	Why   string
}

// Options configure merging.
type Options struct {
	// Tolerance is the maximum distance a subtitle boundary may be moved to
	// snap onto a video cut. Zero disables snapping; boundaries that do not
	// snap are kept exactly.
	Tolerance model.Rat
	// MaxGap is the largest allowed gap between consecutive video samples
	// before the timeline records a discontinuity. Zero disables the check.
	MaxGap model.Rat
}

// BuildOptions carries the per-track inputs to Build.
type BuildOptions struct {
	VideoSamples []Sample
	Subtitles    []model.Track
	Cues         map[string][]Cue
	Timecodes    []TimecodeMark
	Opts         Options
}

// Sample is one video frame on the media clock.
type Sample struct {
	Track     model.Track
	Index     int
	Start     model.Rat
	Duration  model.Rat
	Keyframe  bool
	Origin    string // "index" or "scan", recorded for the hit-rate metric
	Label     string
	SizeBytes uint32
}

// Cue is a subtitle event on the media clock.
type Cue struct {
	Track  model.Track
	Index  int
	Start  model.Rat
	End    model.Rat
	Text   string
	Origin string
}

// TimecodeMark is a timecode label anchored to a media instant.
type TimecodeMark struct {
	Track   model.Track
	Index   int
	Label   string
	Time    model.Rat
	Segment string
}

// Result is the merged, deterministically ordered timeline plus diagnostics.
type Result struct {
	Items      []Item
	Boundaries []Boundary
	// Snapped counts subtitle boundaries moved onto video cuts.
	Snapped int
	// Items and Boundaries are in deterministic order and carry Seq indices
	// assigned after sorting.
	Errors model.Errors
}

// Build merges every track. The output depends only on the values passed in,
// never on map iteration order.
func Build(in BuildOptions) Result {
	var res Result
	// 1. Video samples, already expected in presentation order. A sample with
	// a zero duration is still valid (a gap marker owns its own instant), but
	// a negative one would make the span run backwards.
	for _, s := range in.VideoSamples {
		end := s.Start.Add(s.Duration)
		if s.Duration.Sign() < 0 {
			res.Errors.Add(model.NewError(model.CodeTimelineAlign, "timeline", s.Track.ID, model.InputPos{},
				"sample %d has negative duration %s", s.Index, s.Duration))
			continue
		}
		res.Items = append(res.Items, Item{
			Track: s.Track, Kind: ItemVideo, Key: videoKey(s),
			Span:   model.Span{Start: s.Start, End: end},
			Source: s.Origin, SourceIdx: s.Index, Payload: s.Label,
		})
		res.Boundaries = append(res.Boundaries, Boundary{Time: s.Start, Track: s.Track, Why: "video_cut"})
	}
	// 2. Video cut points for snapping.
	cuts := make([]model.Rat, 0, len(in.VideoSamples))
	for _, s := range in.VideoSamples {
		cuts = append(cuts, s.Start)
	}
	// 3. Subtitle cues, snapped and split at video cuts.
	for _, track := range in.Subtitles {
		cues := in.Cues[track.ID]
		for _, c := range cues {
			start, snapStart := snap(c.Start, cuts, in.Opts.Tolerance)
			end, snapEnd := snap(c.End, cuts, in.Opts.Tolerance)
			if snapStart {
				res.Snapped++
			}
			if snapEnd {
				res.Snapped++
			}
			if end.Cmp(start) <= 0 {
				res.Errors.Add(model.NewError(model.CodeTimelineAlign, "timeline", track.ID, model.InputPos{},
					"cue %d collapses after snapping (%s --> %s)", c.Index, c.Start, c.End))
				continue
			}
			pieces := splitPiece(c, start, end, cuts)
			for i, p := range pieces {
				res.Items = append(res.Items, Item{
					Track: track, Kind: ItemSubtitle,
					Key:    subtitleKey(track.ID, c.Index, i, p.Start, p.End),
					Span:   p,
					Source: c.Origin, SourceIdx: c.Index, Payload: c.Text,
				})
			}
			res.Boundaries = append(res.Boundaries, Boundary{Time: start, Track: track, Why: "subtitle_in"})
			res.Boundaries = append(res.Boundaries, Boundary{Time: end, Track: track, Why: "subtitle_out"})
		}
	}
	// 4. Timecode marks.
	for _, m := range in.Timecodes {
		res.Items = append(res.Items, Item{
			Track: m.Track, Kind: ItemTimecode,
			Key:    timecodeKey(m.Track.ID, m.Index, m.Label, m.Time),
			Span:   model.Span{Start: m.Time, End: m.Time},
			Source: m.Segment, SourceIdx: m.Index, Payload: m.Label,
		})
		res.Boundaries = append(res.Boundaries, Boundary{Time: m.Time, Track: m.Track, Why: "timecode_mark"})
	}
	sortItems(res.Items)
	sortBoundaries(res.Boundaries)
	if !in.Opts.MaxGap.IsZero() {
		res.Errors = append(res.Errors, gapErrors(in.VideoSamples, in.Opts.MaxGap)...)
	}
	return res
}

// videoKey is content-derived so reordering samples cannot collide.
func videoKey(s Sample) string {
	return "video|" + s.Track.ID + "|" + itoa(s.Index) + "|" + s.Start.String()
}

func subtitleKey(track string, cue, piece int, start, end model.Rat) string {
	return "subtitle|" + track + "|" + itoa(cue) + "|" + itoa(piece) + "|" + start.String() + "|" + end.String()
}

func timecodeKey(track string, idx int, label string, t model.Rat) string {
	return "timecode|" + track + "|" + itoa(idx) + "|" + label + "|" + t.String()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// sortItems orders by (start, kind priority, track, key). The key tiebreak
// guarantees a total order, so identical inputs always produce identical seqs.
func sortItems(items []Item) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if c := a.Span.Start.Cmp(b.Span.Start); c != 0 {
			return c < 0
		}
		if a.Kind != b.Kind {
			return kindRank(a.Kind) < kindRank(b.Kind)
		}
		if a.Track.ID != b.Track.ID {
			return a.Track.ID < b.Track.ID
		}
		if a.Span.End.Cmp(b.Span.End) != 0 {
			return a.Span.End.Cmp(b.Span.End) < 0
		}
		return a.Key < b.Key
	})
	for i := range items {
		items[i].Seq = i
	}
}

func kindRank(k ItemKind) int {
	switch k {
	case ItemVideo:
		return 0
	case ItemSubtitle:
		return 1
	case ItemTimecode:
		return 2
	}
	return 3
}

func sortBoundaries(bs []Boundary) {
	sort.SliceStable(bs, func(i, j int) bool {
		if c := bs[i].Time.Cmp(bs[j].Time); c != 0 {
			return c < 0
		}
		if bs[i].Track.ID != bs[j].Track.ID {
			return bs[i].Track.ID < bs[j].Track.ID
		}
		return bs[i].Why < bs[j].Why
	})
}

// snap moves t onto the nearest cut within tolerance. It returns the
// (possibly unchanged) value and whether a snap happened. Ties prefer the
// earlier cut so the choice is deterministic.
func snap(t model.Rat, cuts []model.Rat, tol model.Rat) (model.Rat, bool) {
	if tol.IsZero() || tol.Sign() <= 0 || len(cuts) == 0 {
		return t, false
	}
	lo := sort.Search(len(cuts), func(i int) bool { return cuts[i].Cmp(t) >= 0 })
	best := -1
	var bestDist model.Rat
	consider := func(idx int) {
		if idx < 0 || idx >= len(cuts) {
			return
		}
		d := cuts[idx].Sub(t)
		if d.Sign() < 0 {
			d = model.RatFromInt(0).Sub(d)
		}
		if d.Cmp(tol) > 0 {
			return
		}
		if best == -1 || d.Cmp(bestDist) < 0 || (d.Equal(bestDist) && cuts[idx].Cmp(cuts[best]) < 0) {
			best = idx
			bestDist = d
		}
	}
	consider(lo - 1)
	consider(lo)
	if best == -1 {
		return t, false
	}
	return cuts[best], !cuts[best].Equal(t)
}

// splitPiece divides [start,end) at cut points strictly inside it.
func splitPiece(c Cue, start, end model.Rat, cuts []model.Rat) []model.Span {
	inner := make([]model.Rat, 0, 4)
	for _, cut := range cuts {
		if cut.Cmp(start) > 0 && cut.Cmp(end) < 0 {
			inner = append(inner, cut)
		}
	}
	sort.Slice(inner, func(i, j int) bool { return inner[i].Cmp(inner[j]) < 0 })
	// Deduplicate so identical cut indices cannot produce empty pieces.
	uniq := inner[:0]
	for i, v := range inner {
		if i > 0 && v.Equal(inner[i-1]) {
			continue
		}
		uniq = append(uniq, v)
	}
	bounds := append([]model.Rat{start}, uniq...)
	bounds = append(bounds, end)
	out := make([]model.Span, 0, len(bounds)-1)
	for i := 0; i+1 < len(bounds); i++ {
		out = append(out, model.Span{Start: bounds[i], End: bounds[i+1]})
	}
	return out
}

func gapErrors(samples []Sample, maxGap model.Rat) model.Errors {
	var es model.Errors
	for i := 1; i < len(samples); i++ {
		prevEnd := samples[i-1].Start.Add(samples[i-1].Duration)
		gap := samples[i].Start.Sub(prevEnd)
		if gap.Cmp(maxGap) > 0 {
			es.Add(model.NewError(model.CodeTimelineAlign, "timeline", samples[i].Track.ID, model.InputPos{},
				"gap %s between sample %d and %d exceeds %s s",
				gap, samples[i-1].Index, samples[i].Index, maxGap))
		}
	}
	return es
}
