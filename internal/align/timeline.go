package align

import (
	"fmt"
	"sort"

	"mediadeliver/internal/dash"
	"mediadeliver/internal/hls"
	"mediadeliver/internal/model"
	"mediadeliver/internal/segment"
)

// Scan is one representation's scan result plus identity.
type Scan struct {
	RepID  string
	Path   string
	Digest string
	Result *segment.Result
}

// Input is everything the aligner needs for one delivery.
type Input struct {
	MPD      *dash.MPD
	Playlist *hls.Playlist
	Scans    []Scan
	Inputs   []InputRef
}

type cell struct {
	start, end int64
	discSeq    uint64
	hlsIdx     int
	hlsStart   int64
	mediaSeq   uint64
}

// Build aligns the inputs and returns the manifest draft. A non-nil
// manifest is always returned: unrecoverable inputs produce diagnostics and
// an empty, explicitly non-deliverable summary.
func Build(in Input) *Manifest {
	m := &Manifest{Schema: Schema, Inputs: sortedInputs(in.Inputs)}
	if in.MPD == nil || in.Playlist == nil {
		m.Errors = append(m.Errors, model.NewError(model.CodeAlignInput, "", model.NoOffset,
			model.StageAlign, false, "alignment requires both an MPD and an HLS playlist"))
		return finish(m)
	}
	mpdDigest := digestOf(m.Inputs, "mpd", "")
	cells := cellsOf(in.Playlist)
	if len(cells) == 0 {
		m.Errors = append(m.Errors, model.NewError(model.CodeHLSInvalid, in.Playlist.Path, model.NoOffset,
			model.StageAlign, false, "HLS playlist contains no media segments"))
	}
	scans := map[string]Scan{}
	for _, s := range in.Scans {
		scans[s.RepID] = s
	}
	mediaDigests := map[string]string{}
	for _, ref := range m.Inputs {
		if ref.Role == "media" {
			mediaDigests[ref.Representation] = ref.SHA256
		}
	}
	reps := append([]*dash.Representation(nil), in.MPD.Reps...)
	sort.SliceStable(reps, func(i, j int) bool { return reps[i].ID < reps[j].ID })
	for _, rep := range reps {
		sc := scans[rep.ID]
		m.Representations = append(m.Representations, representationOf(rep, sc, mpdDigest))
		m.Summary.Segments += len(rep.Segments)
	}
	for i, c := range cells {
		m.Intervals = append(m.Intervals, intervalOf(i, c, reps, scans, mpdDigest, mediaDigests, &m.Errors))
	}
	return finish(m)
}

func representationOf(rep *dash.Representation, sc Scan, mpdDigest string) Representation {
	_ = mpdDigest
	r := Representation{
		ID:           rep.ID,
		MimeType:     rep.MimeType,
		Codecs:       rep.Codecs,
		Timescale:    rep.Timescale,
		Bandwidth:    rep.Bandwidth,
		SegmentCount: len(rep.Segments),
		Deliverable:  len(rep.Errors) == 0,
		SourceOffset: model.NoOffset,
		SourcePath:   rep.PathRef,
	}
	if len(rep.Segments) > 0 {
		r.StartNS = rep.Segments[0].StartNS
		last := rep.Segments[len(rep.Segments)-1]
		r.EndNS = last.StartNS + last.DurationNS
	}
	if len(rep.Errors) > 0 {
		r.ErrorCode = string(rep.Errors[0].Code)
		r.SourceOffset = rep.Errors[0].Offset
		r.SourcePath = rep.Errors[0].Path
		r.Deliverable = false
	}
	if sc.Result != nil {
		r.MediaPath = sc.Path
		r.MediaSHA256 = sc.Digest
	}
	return r
}

func intervalOf(index int, c cell, reps []*dash.Representation, scans map[string]Scan, mpdDigest string, mediaDigests map[string]string, errs *[]*model.Error) Interval {
	iv := Interval{
		Index:              index,
		DiscontinuitySeq:   c.discSeq,
		HLSIntervalIndex:   c.hlsIdx,
		HLSIntervalStartNS: c.hlsStart,
		IntervalOffsetNS:   c.start - c.hlsStart,
		StartNS:            c.start,
		EndNS:              c.end,
		HLSMediaSequences:  []uint64{c.mediaSeq},
		Deliverable:        true,
	}
	for _, rep := range reps {
		e, entryErrs := entryOf(rep, scans[rep.ID], c, mpdDigest, mediaDigests[rep.ID], index)
		iv.Entries = append(iv.Entries, e)
		iv.Errors = append(iv.Errors, entryErrs...)
		*errs = append(*errs, entryErrs...)
		if e.DriftNS > iv.MaxDriftNS {
			iv.MaxDriftNS = e.DriftNS
		}
		if !e.Deliverable {
			iv.Deliverable = false
		}
	}
	return iv
}

// entryOf aggregates every representation segment that overlaps the HLS
// interval. Drift is measured at the interval boundaries against the nearest
// representation boundary, so a 4 s HLS interval covered by 0.5 s DASH
// segments aligns exactly.
func entryOf(rep *dash.Representation, sc Scan, c cell, mpdDigest, mediaDigest string, cellIndex int) (Entry, []*model.Error) {
	e := Entry{
		RepresentationID: rep.ID,
		StartNS:          c.start,
		EndNS:            c.end,
		SourceOffset:     model.NoOffset,
		MPDDigest:        mpdDigest,
		MediaDigest:      mediaDigest,
		Deliverable:      true,
	}
	if sc.Result != nil {
		e.MediaDigest = sc.Digest
	}
	var errs []*model.Error
	if len(rep.Errors) > 0 {
		e.Corrupt = true
		e.CorruptReason = string(rep.Errors[0].Code)
		e.SourceOffset = rep.Errors[0].Offset
		e.Deliverable = false
		return e, errs
	}
	overlap := overlapping(rep.Segments, c.start, c.end)
	if len(overlap) == 0 {
		e.Corrupt = true
		e.CorruptReason = string(model.CodeHLSMismatch)
		e.Deliverable = false
		errs = append(errs, model.NewError(model.CodeHLSMismatch, rep.PathRef, rep.SourceOffsetOrZero(),
			model.StageAlign, true,
			fmt.Sprintf("representation %s has no segment covering interval %d", rep.ID, cellIndex)))
		return e, errs
	}
	first := overlap[0]
	e.SourceOffset = first.SourceOffset
	for _, s := range overlap {
		e.SegmentNumbers = append(e.SegmentNumbers, s.Number)
	}
	boundaries := repBoundaries(rep.Segments)
	e.DriftNS = max64(nearestDistance(boundaries, c.start), nearestDistance(boundaries, c.end))
	if e.DriftNS > MaxDriftNS {
		e.Deliverable = false
		errs = append(errs, model.NewError(model.CodeBoundaryDrift, rep.PathRef, first.SourceOffset,
			model.StageAlign, true,
			fmt.Sprintf("representation %s interval %d drifts %dns from the nearest segment boundary",
				rep.ID, cellIndex, e.DriftNS)))
	}
	// A gap between consecutive DASH segments that falls inside this
	// interval means the interval cannot be delivered from this rendition.
	if gap := firstCoverageGap(overlap, c.start, c.end); gap > 0 {
		e.Corrupt = true
		e.CorruptReason = string(model.CodeHLSMismatch)
		e.Deliverable = false
		errs = append(errs, model.NewError(model.CodeHLSMismatch, rep.PathRef, first.SourceOffset,
			model.StageAlign, true,
			fmt.Sprintf("representation %s interval %d has a %dns coverage gap", rep.ID, cellIndex, gap)))
	}
	coveredFrom := overlap[0].StartNS
	coveredTo := overlap[len(overlap)-1].StartNS + overlap[len(overlap)-1].DurationNS
	if coveredFrom > c.start {
		e.DriftNS = max64(e.DriftNS, coveredFrom-c.start)
	}
	if coveredTo < c.end {
		e.DriftNS = max64(e.DriftNS, c.end-coveredTo)
	}
	if sc.Result != nil {
		ordinal := indexOfSegment(rep.Segments, overlap[0].Number)
		if ordinal < 0 || ordinal+len(overlap) > len(sc.Result.Segments) {
			e.Corrupt = true
			e.CorruptReason = string(model.CodeIncompleteSamples)
			e.Deliverable = false
			errs = append(errs, model.NewError(model.CodeIncompleteSamples, sc.Path, first.SourceOffset,
				model.StageScan, true,
				fmt.Sprintf("representation %s has no scan boundaries for segment %d", rep.ID, first.Number)))
			return e, errs
		}
		bs := sc.Result.Segments[ordinal : ordinal+len(overlap)]
		e.MediaOffset = bs[0].Offset
		lastBoundary := bs[len(bs)-1]
		e.MediaSize = lastBoundary.Offset + lastBoundary.Size - bs[0].Offset
		for _, b := range bs {
			e.ScanSampleCount += b.SampleCount
			if b.Corrupt {
				e.Corrupt = true
				e.CorruptReason = b.ErrorCode
				e.Deliverable = false
			}
		}
	}
	e.Deliverable = e.Deliverable && !e.Corrupt
	return e, errs
}

func overlapping(segs []dash.Segment, start, end int64) []dash.Segment {
	var out []dash.Segment
	for _, s := range segs {
		if s.StartNS < end && s.StartNS+s.DurationNS > start {
			out = append(out, s)
		}
	}
	return out
}

func repBoundaries(segs []dash.Segment) []int64 {
	if len(segs) == 0 {
		return nil
	}
	out := make([]int64, 0, len(segs)+1)
	out = append(out, segs[0].StartNS)
	for _, s := range segs {
		out = append(out, s.StartNS+s.DurationNS)
	}
	return out
}

func nearestDistance(boundaries []int64, t int64) int64 {
	if len(boundaries) == 0 {
		return 0
	}
	best := abs64(boundaries[0] - t)
	for _, b := range boundaries[1:] {
		if d := abs64(b - t); d < best {
			best = d
		}
	}
	return best
}

func firstCoverageGap(segs []dash.Segment, start, end int64) int64 {
	cursor := start
	var gap int64
	for _, s := range segs {
		if s.StartNS > cursor {
			gap = max64(gap, min64(s.StartNS, end)-cursor)
		}
		if s.StartNS+s.DurationNS > cursor {
			cursor = s.StartNS + s.DurationNS
		}
	}
	if cursor < end {
		gap = max64(gap, end-cursor)
	}
	return gap
}

// indexOfSegment returns the position of the segment with the given number.
func indexOfSegment(segs []dash.Segment, number uint64) int {
	for i := range segs {
		if segs[i].Number == number {
			return i
		}
	}
	return -1
}

func cellsOf(p *hls.Playlist) []cell {
	out := make([]cell, 0, len(p.Segments))
	for _, s := range p.Segments {
		var ivStart int64
		if s.IntervalIndex >= 0 && s.IntervalIndex < len(p.Intervals) {
			ivStart = p.Intervals[s.IntervalIndex].StartNS
		}
		out = append(out, cell{
			start: s.StartNS, end: s.EndNS(), discSeq: s.DiscontinuitySeq,
			hlsIdx: s.IntervalIndex, hlsStart: ivStart, mediaSeq: s.MediaSequence,
		})
	}
	return out
}

func finish(m *Manifest) *Manifest {
	m.Summary.Representations = len(m.Representations)
	m.Summary.Intervals = len(m.Intervals)
	m.Summary.Diagnostics = len(m.Errors)
	m.Summary.Deliverable = len(m.Intervals) > 0
	for _, iv := range m.Intervals {
		if !iv.Deliverable {
			m.Summary.Deliverable = false
		}
		if iv.MaxDriftNS > m.Summary.MaxDriftNS {
			m.Summary.MaxDriftNS = iv.MaxDriftNS
		}
	}
	model.SortErrors(m.Errors)
	return m
}

func sortedInputs(in []InputRef) []InputRef {
	out := append([]InputRef(nil), in...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Role != out[j].Role {
			return roleOrder(out[i].Role) < roleOrder(out[j].Role)
		}
		if out[i].Representation != out[j].Representation {
			return out[i].Representation < out[j].Representation
		}
		return out[i].Path < out[j].Path
	})
	return out
}

func roleOrder(role string) int {
	switch role {
	case "mpd":
		return 0
	case "hls":
		return 1
	default:
		return 2
	}
}

func digestOf(inputs []InputRef, role, rep string) string {
	for _, in := range inputs {
		if in.Role == role && in.Representation == rep {
			return in.SHA256
		}
	}
	return ""
}

func maxAbs(a, b int64) int64 { return max64(abs64(a), abs64(b)) }
func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
