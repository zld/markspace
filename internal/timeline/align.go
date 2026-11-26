package timeline

import (
	"fmt"

	"continuity/internal/clock"
	"continuity/internal/manifest"
)

// Source labels.
const (
	SourcePrimary = "primary"
	SourceBackup  = "backup"
)

// Segment is one placed chunk on the unified program timeline.
type Segment struct {
	Source         string
	RepID          string
	Index          int
	Sequence       int
	URL            string
	Start          clock.Rat
	Duration       clock.Rat
	End            clock.Rat
	ByteSize       int64
	Line           int
	Discontinuity  bool
}

// Gap is a program-time interval the primary manifest does not cover.
type Gap struct {
	Start    clock.Rat
	End      clock.Rat
	FilledBy *Segment // nil until/unless a backup segment covers it
}

// Timeline is the merged, gap-checked program timeline for one logical
// stream (all representations share the same time grid).
type Timeline struct {
	Segments []Segment
	Gaps     []Gap
	// Recovered counts gaps successfully filled from the backup source.
	Recovered int
}

// Interval overlap helpers operating on exact rationals.

func endOf(s manifest.Segment) clock.Rat {
	e, err := s.Start.Add(s.Duration)
	if err != nil {
		panic(err)
	}
	return e
}

// covers reports whether backup segment b fully covers the half-open
// interval [gs, ge) on the program time axis.
func covers(b manifest.Segment, gs, ge clock.Rat) (bool, error) {
	bStart := b.Start
	bEnd, err := b.Start.Add(b.Duration)
	if err != nil {
		return false, err
	}
	c1, err := bStart.Cmp(gs)
	if err != nil {
		return false, err
	}
	c2, err := bEnd.Cmp(ge)
	if err != nil {
		return false, err
	}
	return c1 <= 0 && c2 >= 0, nil
}

// Build merges one primary manifest representation with the matching backup
// representation. Backup segments may only fill primary gaps whose interval
// they fully cover on the common time axis. An unfillable gap is a hard
// error (KindUnrecoverable): the program must never be silently shortened.
//
// discMark sets Discontinuity on backup segments whose index appears in the
// backup representation's discontinuity list (HLS); nil for DASH.
func Build(primary, backup *manifest.Representation, label string) (*Timeline, error) {
	tl := &Timeline{}
	discSet := map[int]bool{}
	if backup != nil {
		for _, d := range backup.Discontinuities {
			discSet[d] = true
		}
	}

	// 1. Walk the primary in order, recording gaps between neighbors.
	type primaryEntry struct {
		seg manifest.Segment
	}
	var entries []primaryEntry
	var gaps []Gap
	var cursor clock.Rat = clock.FromInt(0)
	for _, s := range primary.Segments {
		c, err := s.Start.Cmp(cursor)
		if err != nil {
			return nil, err
		}
		if c > 0 {
			gapEnd := s.Start
			gaps = append(gaps, Gap{Start: cursor, End: gapEnd})
		} else if c < 0 {
			return nil, clock.New(clock.KindInvalidInput, "align",
				"%s: segment index %d starts before timeline cursor", label, s.Index)
		}
		entries = append(entries, primaryEntry{seg: s})
		next, err := s.Start.Add(s.Duration)
		if err != nil {
			return nil, err
		}
		cursor = next
	}
	programEnd := cursor

	// 2. Fill each gap strictly from time-overlapping backup coverage.
	for gi := range gaps {
		g := &gaps[gi]
		if backup == nil {
			return nil, unfillable(g, label, "no backup source available")
		}
		var chosen *manifest.Segment
		for bi := range backup.Segments {
			b := &backup.Segments[bi]
			ok, err := covers(*b, g.Start, g.End)
			if err != nil {
				return nil, err
			}
			if ok {
				bcopy := *b
				chosen = &bcopy
				break
			}
		}
		if chosen == nil {
			return nil, unfillable(g, label,
				"no backup segment temporally covers the gap")
		}
		filled := backupToSegment(*chosen, discSet[chosen.Index])
		g.FilledBy = &filled
		tl.Recovered++
	}

	// 3. Emit the merged sequence: gap-fillers first if they precede the
	//    next primary segment, then the primary segments themselves.
	gapIdx := 0
	for _, e := range entries {
		for gapIdx < len(gaps) {
			c, err := gaps[gapIdx].Start.Cmp(e.seg.Start)
			if err != nil {
				return nil, err
			}
			if c >= 0 {
				break
			}
			tl.Gaps = append(tl.Gaps, gaps[gapIdx])
			tl.Segments = append(tl.Segments, *gaps[gapIdx].FilledBy)
			gapIdx++
		}
		tl.Segments = append(tl.Segments, primaryToSegment(e.seg))
	}
	for ; gapIdx < len(gaps); gapIdx++ {
		tl.Gaps = append(tl.Gaps, gaps[gapIdx])
		tl.Segments = append(tl.Segments, *gaps[gapIdx].FilledBy)
	}

	// 4. Sanity: emitted coverage must reach the full program end.
	if len(tl.Segments) == 0 {
		return nil, clock.New(clock.KindInvalidInput, "align",
			"%s: empty timeline", label)
	}
	last := tl.Segments[len(tl.Segments)-1]
	c, err := last.End.Cmp(programEnd)
	if err != nil {
		return nil, err
	}
	if c < 0 {
		return nil, clock.New(clock.KindUnrecoverable, "align",
			"%s: merged timeline ends at %s before program end %s",
			label, last.End, programEnd)
	}
	return tl, nil
}

func primaryToSegment(s manifest.Segment) Segment {
	return Segment{
		Source:   SourcePrimary,
		RepID:    "",
		Index:    s.Index,
		Sequence: s.Sequence,
		URL:      s.URL,
		Start:    s.Start,
		Duration: s.Duration,
		End:      endOf(s),
		ByteSize: s.ByteSize,
		Line:     s.Line,
	}
}

func backupToSegment(s manifest.Segment, disc bool) Segment {
	seg := primaryToSegment(s)
	seg.Source = SourceBackup
	seg.Discontinuity = disc
	return seg
}

func unfillable(g *Gap, label, reason string) error {
	return clock.New(clock.KindUnrecoverable, "align",
		"%s: gap [%s,%s) cannot be filled: %s",
		label, g.Start, g.End, reason)
}

// PairRepresentations matches primary and backup representations by ID.
// Every primary ID must have a backup counterpart. A nil backup manifest is
// allowed only when the primary has no gaps (checked later inside Build).
func PairRepresentations(primary, backup *manifest.Manifest) (
	map[string]*manifest.Representation, error) {
	out := map[string]*manifest.Representation{}
	if backup == nil {
		return out, nil
	}
	byID := map[string]*manifest.Representation{}
	for i := range backup.Representations {
		r := &backup.Representations[i]
		byID[r.ID] = r
	}
	for i := range primary.Representations {
		r := &primary.Representations[i]
		match, ok := byID[r.ID]
		if !ok {
			return nil, clock.New(clock.KindInvalidInput, "align",
				"representation %q has no backup counterpart", r.ID)
		}
		out[r.ID] = match
	}
	return out, nil
}

// MaxAlignMs computes the largest absolute start-time mismatch in
// milliseconds across representations sharing the same segment index.
func MaxAlignMs(timelines map[string]*Timeline) (clock.Rat, error) {
	maxAbs := clock.FromInt(0)
	var ref *Timeline
	for _, tl := range timelines {
		ref = tl
		break
	}
	if ref == nil {
		return maxAbs, nil
	}
	for id, tl := range timelines {
		if tl == ref {
			continue
		}
		n := len(ref.Segments)
		if len(tl.Segments) < n {
			n = len(tl.Segments)
		}
		for i := 0; i < n; i++ {
			diff, err := ref.Segments[i].Start.Sub(tl.Segments[i].Start)
			if err != nil {
				return clock.Rat{}, err
			}
			if diff.Num < 0 {
				diff.Num = -diff.Num
			}
			ms, err := diff.Scale(1000)
			if err != nil {
				return clock.Rat{}, err
			}
			c, err := ms.Cmp(maxAbs)
			if err != nil {
				return clock.Rat{}, err
			}
			if c > 0 {
				maxAbs = ms
			}
		}
		_ = id
	}
	return maxAbs, nil
}

// String renders a short debug view.
func (t *Timeline) String() string {
	return fmt.Sprintf("Timeline(segments=%d gaps=%d recovered=%d)",
		len(t.Segments), len(t.Gaps), t.Recovered)
}
