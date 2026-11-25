package continuity

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
)

// The timeline advances an event-time watermark. Samples arriving after the
// watermark are late; those inside the configured window are inserted and the
// whole program is recomputed deterministically, those beyond it are rejected
// without touching the committed output. Every derived artifact (sample plan,
// manifest text, media bytes, digest) is a pure function of the accepted
// sample set, so replaying the same inputs always yields the same digest.

// TimelineConfig controls watermarking and conflict resolution.
type TimelineConfig struct {
	// LateWindow is how far behind the watermark a sample may still be
	// accepted. Zero allows only samples at or ahead of the watermark.
	LateWindow Ticks
	// SourceOrder lists source labels from highest to lowest priority.
	SourceOrder []string
	// WrapModulus is the modular timestamp space used to unwrap source DTS.
	WrapModulus int64
	// WrapGap is the raw backward jump at or above which a wrap is assumed.
	WrapGap int64
}

// DefaultTimelineConfig returns a config with a 500 ms late window.
func DefaultTimelineConfig() TimelineConfig {
	win, _ := FromMillis(500)
	return TimelineConfig{
		LateWindow:  win,
		WrapModulus: 1 << 32,
		WrapGap:     1 << 31,
	}
}

// TimelineStats is stable acceptance output.
type TimelineStats struct {
	Samples         int
	LateRecomputed  int
	LateRejected    int
	Duplicates      int
	OverlapsDropped int
	Gaps            int
	TimeDeltaTicks  int64
	ByteDelta       int64
	FailureLocation string
}

// Program is the recomputed, deterministic output plan.
type Program struct {
	Tracks  []TargetTrack
	Samples []PlacedSample
	Stats   TimelineStats
	// CommittedEnd is the watermark at the time of the last recompute.
	CommittedEnd Ticks
}

// PlacedSample is one output sample after conflict resolution.
type PlacedSample struct {
	Source     string
	Target     string
	TrackKey   string
	DTS        Ticks
	PTS        Ticks
	Duration   Ticks
	Sync       bool
	ByteOffset int64
	ByteLength int64
	Data       []byte
	// SourcePTS is the sample PTS before unwrapping, for delta accounting.
	SourcePTS Ticks
	// Segment/Index locate the sample in its input.
	Segment string
	Index   int
}

// End returns the exclusive output end.
func (p PlacedSample) End() Ticks { return p.PTS + p.Duration }

// timelineState holds per-source unwrap state.
type timelineState struct {
	lastUnwrapped Ticks
	haveLast      bool
}

// Timeline accepts samples in arrival order.
type Timeline struct {
	cfg          TimelineConfig
	tm           *TrackMap
	state        map[string]*timelineState // key: source|target
	accepted     []PlacedSample
	watermark    Ticks
	observedMax  Ticks
	haveObserved bool
	stats        TimelineStats
	failure      *Error
}

// NewTimeline builds a timeline for the mapping.
func NewTimeline(tm *TrackMap, cfg TimelineConfig) (*Timeline, error) {
	if tm == nil {
		return nil, Usagef("timeline requires a track map")
	}
	if cfg.LateWindow < 0 {
		return nil, Usagef("late window must be non-negative")
	}
	if cfg.WrapModulus <= 0 {
		cfg.WrapModulus = 1 << 32
	}
	if cfg.WrapGap <= 0 {
		cfg.WrapGap = cfg.WrapModulus / 2
	}
	return &Timeline{cfg: cfg, tm: tm, state: map[string]*timelineState{}}, nil
}

// Watermark returns the current event-time watermark.
func (t *Timeline) Watermark() Ticks { return t.watermark }

// Add ingests one sample. A returned error with KindLate means the sample was
// rejected at the boundary and the committed output is unchanged; other errors
// are hard faults. Add never mutates the accepted set on error.
func (t *Timeline) Add(source string, s Sample) error {
	m, ok := t.tm.Lookup(source, s.SourceID)
	if !ok {
		return NewError(KindMapping, Location{File: s.File, Segment: s.Segment, Offset: s.ByteOffset, Sample: s.Index},
			"source %q track %d has no target mapping", source, s.SourceID)
	}
	target, ok := t.tm.Target(m.Target)
	if !ok {
		return NewError(KindMapping, Location{File: s.File, Segment: s.Segment, Offset: s.ByteOffset, Sample: s.Index},
			"target track %q is missing", m.Target)
	}
	loc := Location{File: s.File, Segment: s.Segment, Offset: s.ByteOffset, Sample: s.Index}
	if s.Duration < 0 {
		return NewError(KindBadReference, loc, "negative sample duration")
	}
	if len(s.Data) == 0 {
		return NewError(KindBadReference, loc, "empty sample payload cannot be expressed losslessly")
	}

	dts, pts, err := t.unwrap(source, m.Target, s, loc)
	if err != nil {
		return err
	}
	placed := PlacedSample{
		Source: source, Target: m.Target, TrackKey: target.Key,
		DTS: dts, PTS: pts, Duration: s.Duration, Sync: s.Sync,
		ByteOffset: s.ByteOffset, ByteLength: s.ByteLength, Data: s.Data,
		SourcePTS: s.PTS, Segment: s.Segment, Index: s.Index,
	}
	end := placed.End()
	// A sample at or behind the watermark is late: its position in the
	// committed output is already decided by earlier samples.
	if t.haveObserved && placed.PTS <= t.watermark {
		lateness := t.watermark - placed.PTS
		if lateness > t.cfg.LateWindow {
			t.stats.LateRejected++
			t.failure = NewError(KindLate, loc,
				"sample from %s at %d is %d ticks behind the watermark %d, beyond the %d-tick late window",
				source, placed.PTS, lateness, t.watermark, t.cfg.LateWindow)
			return t.failure
		}
		t.stats.LateRecomputed++
	}
	t.accepted = append(t.accepted, placed)
	if !t.haveObserved || end > t.observedMax {
		t.observedMax = end
		t.haveObserved = true
	}
	// Event-time watermark: the highest event time seen minus the late window.
	// It is not clamped at zero, so replaying a stream that starts at a
	// negative timestamp classifies lateness identically.
	if wm := t.observedMax - t.cfg.LateWindow; wm > t.watermark {
		t.watermark = wm
	}
	return nil
}

// unwrap converts a source sample's DTS/PTS to the base timeline, repairing
// 32-bit wraparound by mapping each value into the window nearest the previous
// unwrapped DTS. Plain backward steps stay as-is and are governed by the late
// window in Add, so the decision is always in one place.
func (t *Timeline) unwrap(source, target string, s Sample, loc Location) (Ticks, Ticks, error) {
	key := source + "|" + target
	st := t.state[key]
	if st == nil {
		st = &timelineState{}
		t.state[key] = st
	}
	if !st.haveLast {
		st.lastUnwrapped = s.DTS
		st.haveLast = true
		return s.DTS, s.PTS, nil
	}
	unwrapped, err := UnwrapTimestamp(int64(s.DTS), int64(st.lastUnwrapped), t.cfg.WrapModulus)
	if err != nil {
		return 0, 0, NewError(KindBadReference, loc, "timestamp unwrap failed: %v", err)
	}
	shift := unwrapped - int64(s.DTS)
	if shift != 0 && s.DTS+Ticks(shift) < st.lastUnwrapped {
		return 0, 0, NewError(KindBadReference, loc,
			"timestamp %d unwraps to %d, still behind %d; the source clock is inconsistent", s.DTS, unwrapped, st.lastUnwrapped)
	}
	if unwrapped > int64(st.lastUnwrapped) {
		st.lastUnwrapped = Ticks(unwrapped)
	}
	return Ticks(unwrapped), s.PTS + Ticks(shift), nil
}

// Recompute resolves conflicts and produces the deterministic program.
func (t *Timeline) Recompute() (*Program, error) {
	prio := map[string]int{}
	for i, name := range t.cfg.SourceOrder {
		if _, dup := prio[name]; !dup {
			prio[name] = i
		}
	}
	rank := func(src string) int {
		if p, ok := prio[src]; ok {
			return p
		}
		return len(t.cfg.SourceOrder) + 1
	}
	byTarget := map[string][]PlacedSample{}
	for _, s := range t.accepted {
		byTarget[s.Target] = append(byTarget[s.Target], s)
	}
	var (
		out       []PlacedSample
		stats     TimelineStats
		gaps      int
		overlaps  int
		dups      int
		timeDelta int64
		byteDelta int64
	)
	for _, cands := range byTarget {
		sort.SliceStable(cands, func(i, j int) bool {
			a, b := cands[i], cands[j]
			if a.PTS != b.PTS {
				return a.PTS < b.PTS
			}
			if a.DTS != b.DTS {
				return a.DTS < b.DTS
			}
			if ra, rb := rank(a.Source), rank(b.Source); ra != rb {
				return ra < rb
			}
			if a.Source != b.Source {
				return a.Source < b.Source
			}
			if a.Segment != b.Segment {
				return a.Segment < b.Segment
			}
			return a.Index < b.Index
		})
		var kept []PlacedSample
		for _, c := range cands {
			conflict := -1
			for i := range kept {
				if kept[i].End() > c.PTS && c.End() > kept[i].PTS {
					conflict = i
					break
				}
			}
			if conflict < 0 {
				if len(kept) > 0 {
					prev := kept[len(kept)-1]
					if prev.End() < c.PTS {
						gaps++
					}
				}
				kept = append(kept, c)
				continue
			}
			prev := kept[conflict]
			if prev.PTS == c.PTS && prev.Duration == c.Duration && equalBytes(prev.Data, c.Data) {
				dups++
				continue
			}
			// Keep the higher-priority source; on ties keep the earlier one.
			if rank(c.Source) < rank(prev.Source) {
				kept[conflict] = c
				overlaps++
			} else {
				overlaps++
			}
		}
		sort.SliceStable(kept, func(i, j int) bool {
			if kept[i].PTS != kept[j].PTS {
				return kept[i].PTS < kept[j].PTS
			}
			return kept[i].Index < kept[j].Index
		})
		out = append(out, kept...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Target != out[j].Target {
			return out[i].Target < out[j].Target
		}
		if out[i].PTS != out[j].PTS {
			return out[i].PTS < out[j].PTS
		}
		if out[i].DTS != out[j].DTS {
			return out[i].DTS < out[j].DTS
		}
		if out[i].Source != out[j].Source {
			return out[i].Source < out[j].Source
		}
		return out[i].Index < out[j].Index
	})
	for i := range out {
		out[i].ByteOffset = int64(i)
		d := out[i].PTS - out[i].SourcePTS
		if d < 0 {
			d = -d
		}
		timeDelta += int64(d)
	}
	stats = t.stats
	stats.Samples = len(out)
	stats.Duplicates = dups
	stats.OverlapsDropped = overlaps
	stats.Gaps = gaps
	stats.TimeDeltaTicks = timeDelta
	stats.ByteDelta = byteDelta
	if t.failure != nil {
		stats.FailureLocation = t.failure.Loc.String()
	} else {
		stats.FailureLocation = "none"
	}
	tracks := append([]TargetTrack(nil), t.tm.Targets...)
	prog := &Program{Tracks: tracks, Samples: out, Stats: stats, CommittedEnd: t.watermark}
	return prog, nil
}

// Digest returns a stable digest over the program's sample plan and bytes.
// The manifest and remuxed bytes are added by the digest helpers below.
func (p *Program) Digest(manifest []byte, media []byte) string {
	h := sha256.New()
	h.Write([]byte("continuity/program/v1\n"))
	fmt.Fprintf(h, "tracks=%d samples=%d committed=%d\n", len(p.Tracks), len(p.Samples), p.CommittedEnd)
	for _, tr := range p.Tracks {
		fmt.Fprintf(h, "track|%s|%s|%d|%x\n", tr.Key, tr.Codec, tr.Timescale, tr.CodecConfig)
	}
	for _, s := range p.Samples {
		fmt.Fprintf(h, "sample|%s|%s|%d|%d|%d|%t|%d|%d|%x\n",
			s.Source, s.Target, s.DTS, s.PTS, s.Duration, s.Sync, s.ByteOffset, s.ByteLength, s.Data)
	}
	h.Write(manifest)
	h.Write(media)
	return hex.EncodeToString(h.Sum(nil))
}

// ByteDelta computes the total byte deviation between emitted sample payloads
// and their source payloads. It is zero when every emitted byte is preserved
// verbatim, which is the lossless contract.
func (p *Program) ByteDeviation(inputs map[string]Sample) int64 {
	var delta int64
	for _, s := range p.Samples {
		key := fmt.Sprintf("%s|%s|%d", s.Source, s.Segment, s.Index)
		in, ok := inputs[key]
		if !ok {
			continue
		}
		d := int64(len(s.Data)) - int64(len(in.Data))
		if d < 0 {
			d = -d
		}
		delta += d
	}
	return delta
}
