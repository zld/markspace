package cmaf

import (
	"math"
	"sort"
	"time"

	"replay/internal/clock"
	"replay/internal/model"
)

// Segment is one usable fragment placed on the rebuilt media timeline.
type Segment struct {
	Frag     *Fragment
	Start    time.Duration // media time of the first sample
	Dur      time.Duration
	RTPStart uint32 // unified RTP timestamp at segment start
}

// Interval is a half-open media time range [Start, End).
type Interval struct {
	Start time.Duration
	End   time.Duration
}

// Timeline is the media time base rebuilt from fragment metadata.
// Ranges that cannot be determined are marked missing, never stitched.
type Timeline struct {
	Segments    []Segment
	Missing     []Interval
	End         time.Duration
	SeqGaps     int // count of missing mfhd sequence numbers
	Duplicates  int // count of dropped duplicate fragments
	Regressions int // count of tfdt regressions
}

// BuildTimeline reconstructs the media timeline from parsed fragments.
// nil entries represent fragments that failed to parse entirely; the hole
// they leave is detected through the tfdt continuity check. rep collects
// gap, duplicate and regression issues.
func BuildTimeline(frags []*Fragment, init *Init, rep *model.Report) *Timeline {
	tl := &Timeline{}
	rate := primaryTimescale(init)
	if rate == 0 {
		rep.Addf(model.CodeMissingMedia, model.SevFatal, "", -1, "no usable track timescale")
		return tl
	}
	toDur := func(units uint64) time.Duration {
		d, err := clock.ToDuration(units, rate)
		if err != nil {
			rep.Addf(model.CodeMissingMedia, model.SevFatal, "", -1, "%s", err)
			return 0
		}
		return d
	}

	valid := make([]*Fragment, 0, len(frags))
	for _, f := range frags {
		if f != nil {
			valid = append(valid, f)
		}
	}
	// Order by mfhd sequence number, stable by file order; never trust
	// file order alone.
	sort.SliceStable(valid, func(i, j int) bool { return valid[i].Seq < valid[j].Seq })

	clk, err := clock.New(rate, 0x5245504c) // "REPL"
	if err != nil {
		rep.Addf(model.CodeMissingMedia, model.SevFatal, "", -1, "%s", err)
		return tl
	}

	seen := map[uint32]bool{}
	var expected uint64
	var haveExpected bool
	var prevSeq uint32
	for _, f := range valid {
		if seen[f.Seq] {
			tl.Duplicates++
			rep.Addf(model.CodeMFHDDup, model.SevWarn, f.Path, -1,
				"duplicate mfhd sequence number %d dropped", f.Seq)
			continue
		}
		seen[f.Seq] = true
		if haveExpected && f.Seq > prevSeq+1 {
			gap := int(f.Seq - prevSeq - 1)
			tl.SeqGaps += gap
			rep.Addf(model.CodeMFHDGap, model.SevWarn, f.Path, -1,
				"mfhd sequence gap: %d number(s) missing before %d", gap, f.Seq)
		}
		prevSeq = f.Seq

		if !f.HasVideo {
			// A fragment without a video traf (e.g. an audio-only tail
			// fragment from unequal track durations) carries no video
			// timeline information; it is skipped, not treated as damage.
			// A fragment with no decode times at all is undeterminable.
			if !f.HasAudio {
				rep.Addf(model.CodeMissingMedia, model.SevWarn, f.Path, -1,
					"fragment seq %d has no decode times; span undeterminable", f.Seq)
			}
			continue
		}
		if haveExpected {
			switch {
			case f.Tfdt < expected:
				tl.Regressions++
				end := f.Tfdt + f.VideoDur
				if end > expected {
					end = expected
				}
				rep.Addf(model.CodeTfdtRegression, model.SevWarn, f.Path, -1,
					"tfdt %d regresses before expected %d; span marked missing", f.Tfdt, expected)
				tl.Missing = append(tl.Missing, Interval{Start: toDur(f.Tfdt), End: toDur(end)})
				continue // keep the established timeline; do not stitch
			case f.Tfdt > expected:
				tl.Missing = append(tl.Missing, Interval{Start: toDur(expected), End: toDur(f.Tfdt)})
				rep.Addf(model.CodeMissingMedia, model.SevWarn, f.Path, -1,
					"media missing in [%d, %d) before seq %d", expected, f.Tfdt, f.Seq)
			}
		}
		if f.Damaged {
			tl.Missing = append(tl.Missing, Interval{Start: toDur(f.Tfdt), End: toDur(f.Tfdt + f.VideoDur)})
			rep.Addf(model.CodeMissingMedia, model.SevWarn, f.Path, -1,
				"damaged fragment seq %d: span [%d, %d) marked missing", f.Seq, f.Tfdt, f.Tfdt+f.VideoDur)
		} else {
			if f.VideoDur > math.MaxUint32 {
				rep.Addf(model.CodeMissingMedia, model.SevWarn, f.Path, -1,
					"fragment seq %d duration %d overflows RTP clock", f.Seq, f.VideoDur)
			} else {
				tl.Segments = append(tl.Segments, Segment{
					Frag:     f,
					Start:    toDur(f.Tfdt),
					Dur:      toDur(f.VideoDur),
					RTPStart: clk.Advance(uint32(f.VideoDur)),
				})
			}
		}
		expected = f.Tfdt + f.VideoDur
		haveExpected = true
	}
	if haveExpected {
		tl.End = toDur(expected)
	}
	return tl
}

// primaryTimescale returns the video timescale, falling back to audio.
func primaryTimescale(init *Init) uint32 {
	if v := init.Video(); v != nil {
		return v.Timescale
	}
	if a := init.Audio(); a != nil {
		return a.Timescale
	}
	return 0
}

// MissingIn returns missing intervals intersecting [start, end).
func (tl *Timeline) MissingIn(start, end time.Duration) []Interval {
	var out []Interval
	for _, m := range tl.Missing {
		if m.End > start && m.Start < end {
			out = append(out, m)
		}
	}
	return out
}

// SegmentsIn returns usable segments overlapping [start, end), in order.
func (tl *Timeline) SegmentsIn(start, end time.Duration) []Segment {
	var out []Segment
	for _, s := range tl.Segments {
		if s.Start+s.Dur > start && s.Start < end {
			out = append(out, s)
		}
	}
	return out
}
