package core

import (
	"sort"

	"continuity/internal/timebase"
)

// Counter moduli: MPEG PTS is 33-bit, RTP timestamps 32-bit.
const (
	PTSModulus = int64(1) << 33
	RTPModulus = int64(1) << 32
)

const (
	// dupToleranceTicks is the normalized-time equality window for
	// duplicate detection (0.5 ms).
	dupToleranceTicks = int64(45)
	// gapSlackTicks is the tolerated timeline jitter before a gap is
	// declared (50 ms).
	gapSlackTicks = int64(4500)
	// lateBudgetTicks: arrivals later than mediaRef+budget count as late.
	lateBudgetTicks = int64(90000)
)

// Decision is the per-record outcome.
type Decision string

const (
	DecEmitted     Decision = "emitted"
	DecDuplicate   Decision = "duplicate"
	DecUnrecovered Decision = "unrecovered"
)

// Record is one arrived media segment (manifest entry + parsed container).
type Record struct {
	Index      int64 // media sequence from the manifest
	Source     string
	SourceID   string // origin identity ("primary", "backup", ...)
	Line       int    // manifest line
	ArrivalRef int64  // reference-clock arrival ticks
	Info       *SegmentInfo
}

// Item is one mapping row (emitted, duplicate or unrecovered marker).
type Item struct {
	Index       int64
	Source      string
	Line        int
	Output      int
	Decision    Decision
	MediaRef    int64
	ExpectedRef int64
	TimeDeltaUs int64
	ByteOffset  int64
	ByteDelta   int64
	DelayMs     int64
	Fingerprint [32]byte
	Note        string
}

// Summary aggregates one run.
type Summary struct {
	Mapped, Emitted, Duplicate, Lost, Late, Resumed int
	GapMs                                           int64
	P50DelayMs                                      int64
	DriftPPM                                        int64
	MaxAbsTimeDeltaUs                               int64
	MaxAbsByteDelta                                 int64
}

// Input bundles records and RTP events for the kernel.
type Input struct {
	Records []Record
	Events  []Event
}

// Result is the kernel output: ordered mapping rows plus summary.
type Result struct {
	Items   []Item
	Summary Summary
}

// mapped is a record with its unwrapped, reference-mapped media time.
type mapped struct {
	rec       Record
	unwrapped int64
	mediaRef  int64
	dur       int64
}

// row is a mapped record flagged as duplicate or not.
type row struct {
	m   mapped
	dup bool
}

// recordOrder returns record indices sorted by (Index, ArrivalRef, Source).
func recordOrder(records []Record) []int {
	ord := make([]int, len(records))
	for i := range ord {
		ord[i] = i
	}
	sort.SliceStable(ord, func(a, b int) bool {
		ra, rb := records[ord[a]], records[ord[b]]
		if ra.Index != rb.Index {
			return ra.Index < rb.Index
		}
		if ra.ArrivalRef != rb.ArrivalRef {
			return ra.ArrivalRef < rb.ArrivalRef
		}
		return ra.Source < rb.Source
	})
	return ord
}

// UnwrapRecords unwraps every record's raw base time into the shared
// source-clock epoch established by the RTP events, and fits the clock
// model (offset+drift) from the lifted anchors. PTS (mod 2^33) and RTP
// (mod 2^32) are the same source clock reduced by different moduli, so
// the PTS unwrapper is seeded into the RTP-established epoch and anchors
// are lifted into the PTS domain by a constant multiple of 2^32.
//
// The returned slice aligns with the input records order; maxSrc is the
// largest lifted anchor source tick. Fixtures and callers that need
// kernel-consistent reference times use the returned clock.
func UnwrapRecords(events []Event, records []Record) (unwrapped []int64, clk *timebase.Clock, maxSrc int64, err error) {
	rtpUW := timebase.NewUnwrapper(RTPModulus)
	rtpVals := make([]int64, 0, len(events))
	for i, ev := range events {
		src, uerr := rtpUW.Unwrap(int64(ev.RTPTime))
		if uerr != nil {
			return nil, nil, 0, NewError(StageTimebase, CodeAmbiguousJump, "events", 0, int64(i), "rtp timestamp", uerr)
		}
		if i == 0 || src > maxSrc {
			maxSrc = src
		}
		rtpVals = append(rtpVals, src)
	}
	ord := recordOrder(records)
	ptsUW := timebase.NewUnwrapper(PTSModulus)
	if len(rtpVals) > 0 && len(ord) > 0 {
		seed := rtpVals[0]
		firstRaw := records[ord[0]].Info.BaseTime
		for d := int64(0); d*RTPModulus < PTSModulus; d++ {
			if (rtpVals[0]+d*RTPModulus)%PTSModulus == firstRaw%PTSModulus {
				seed = rtpVals[0] + d*RTPModulus
				break
			}
		}
		ptsUW.Seed(seed)
	}
	clk = &timebase.Clock{}
	unwrapped = make([]int64, len(records))
	for i, oi := range ord {
		r := records[oi]
		un, uerr := ptsUW.Unwrap(r.Info.BaseTime)
		if uerr != nil {
			return nil, nil, 0, NewError(StageTimebase, CodeAmbiguousJump, r.Source, r.Line, -1, "pts", uerr)
		}
		if i == 0 && len(rtpVals) > 0 {
			lift := roundDiv(un-rtpVals[0], RTPModulus)
			for j, rv := range rtpVals {
				clk.AddAnchor(timebase.Anchor{Source: rv + lift*RTPModulus, Ref: events[j].ArrivalRef})
			}
			maxSrc += lift * RTPModulus
		}
		unwrapped[oi] = un
	}
	return unwrapped, clk, maxSrc, nil
}

// Build reconstructs continuity state from records and events.
// Records are sorted by media time; arrival order is only used for
// duplicate tie-breaking and byte-displacement accounting.
func Build(in Input) (*Result, error) {
	unwrapped, clk, maxSrc, err := UnwrapRecords(in.Events, in.Records)
	if err != nil {
		return nil, err
	}
	if len(in.Records) > 0 && clk.Anchors() == 0 {
		return nil, NewError(StageTimebase, CodeInput, in.Records[0].Source, in.Records[0].Line, -1,
			"no sync anchors", nil)
	}
	drift, hasClock := clk.DriftPPM()

	// Map records to reference time in media-sequence order.
	ord := recordOrder(in.Records)
	ms := make([]mapped, 0, len(ord))
	for _, oi := range ord {
		r := in.Records[oi]
		ref, _ := clk.ToRef(unwrapped[oi])
		var dur int64
		for _, s := range r.Info.Samples {
			dur += s.Duration // actual VFR intervals, never re-quantized
		}
		ms = append(ms, mapped{rec: r, unwrapped: unwrapped[oi], mediaRef: ref, dur: dur})
	}

	// Media-time order; arrival time breaks ties (earliest wins dup).
	sort.SliceStable(ms, func(i, j int) bool {
		if ms[i].mediaRef != ms[j].mediaRef {
			return ms[i].mediaRef < ms[j].mediaRef
		}
		if ms[i].rec.ArrivalRef != ms[j].rec.ArrivalRef {
			return ms[i].rec.ArrivalRef < ms[j].rec.ArrivalRef
		}
		return ms[i].rec.Source < ms[j].rec.Source
	})

	// Duplicate detection: same track + fingerprint + normalized time.
	res := &Result{}
	res.Summary.Mapped = len(ms)
	res.Summary.DriftPPM = drift
	rows := make([]row, 0, len(ms))
	for _, c := range ms {
		kept := -1
		for i, r := range rows {
			if r.dup {
				continue
			}
			if r.m.rec.Info.TrackID == c.rec.Info.TrackID &&
				r.m.rec.Info.Fingerprint == c.rec.Info.Fingerprint &&
				abs64(r.m.mediaRef-c.mediaRef) <= dupToleranceTicks {
				kept = i
				break
			}
		}
		if kept >= 0 {
			rows = append(rows, row{m: c, dup: true})
			res.Summary.Duplicate++
		} else {
			rows = append(rows, row{m: c})
		}
	}

	// Expected gapless timeline from actual durations; gaps -> lost.
	var emittedDur []int64
	for _, r := range rows {
		if !r.dup {
			emittedDur = append(emittedDur, r.m.dur)
		}
	}
	medDur := median(emittedDur)
	var expectedRef, expectedByte int64
	arrivalByte := arrivalByteOffsets(rows)
	out := 0
	for _, r := range rows {
		m := r.m
		delayMs := (m.rec.ArrivalRef - m.mediaRef) * 1000 / timebase.TicksPerSecond
		if r.dup {
			res.Items = append(res.Items, Item{
				Index: m.rec.Index, Source: m.rec.Source, Line: m.rec.Line,
				Output: -1, Decision: DecDuplicate, MediaRef: m.mediaRef,
				DelayMs: delayMs, Fingerprint: m.rec.Info.Fingerprint,
				Note: "same track+fingerprint+time as earlier arrival; kept earliest",
			})
			continue
		}
		if out == 0 {
			expectedRef = m.mediaRef
			expectedByte = 0
		} else if m.mediaRef-expectedRef > gapSlackTicks {
			gap := m.mediaRef - expectedRef
			lost := 1
			if medDur > 0 {
				lost = int((gap + medDur/2) / medDur)
				if lost < 1 {
					lost = 1
				}
			}
			res.Summary.Lost += lost
			res.Summary.GapMs += gap * 1000 / timebase.TicksPerSecond
			if hasClock && m.unwrapped <= maxSrc {
				res.Summary.Resumed++
			}
			res.Items = append(res.Items, Item{
				Index: -1, Source: "-", Output: -1, Decision: DecUnrecovered,
				MediaRef: expectedRef, ExpectedRef: expectedRef,
				Note: "unrecovered interval",
			})
			// Resume from the next valid anchor: re-anchor the expected
			// timeline; the gap itself stays marked unrecovered.
			expectedRef = m.mediaRef
		}
		deltaUs := ticksToUs(m.mediaRef - expectedRef)
		byteDelta := arrivalByte[m.rec.Source] - expectedByte
		if a := abs64(deltaUs); a > res.Summary.MaxAbsTimeDeltaUs {
			res.Summary.MaxAbsTimeDeltaUs = a
		}
		if a := abs64(byteDelta); a > res.Summary.MaxAbsByteDelta {
			res.Summary.MaxAbsByteDelta = a
		}
		if m.rec.ArrivalRef-m.mediaRef > lateBudgetTicks {
			res.Summary.Late++
		}
		res.Items = append(res.Items, Item{
			Index: m.rec.Index, Source: m.rec.Source, Line: m.rec.Line,
			Output: out, Decision: DecEmitted, MediaRef: m.mediaRef,
			ExpectedRef: expectedRef, TimeDeltaUs: deltaUs,
			ByteOffset: expectedByte, ByteDelta: byteDelta,
			DelayMs: delayMs, Fingerprint: m.rec.Info.Fingerprint,
		})
		expectedRef += m.dur
		expectedByte += m.rec.Info.Size
		out++
	}
	res.Summary.Emitted = out
	res.Summary.P50DelayMs = percentile(emittedDelays(res.Items), 50)
	return res, nil
}

// arrivalByteOffsets maps each record source to the cumulative byte
// position it would occupy if emitted segments were concatenated in
// arrival order instead of media order.
func arrivalByteOffsets(rows []row) map[string]int64 {
	type ent struct {
		source  string
		arrival int64
		size    int64
	}
	var ents []ent
	for _, r := range rows {
		if r.dup {
			continue
		}
		ents = append(ents, ent{r.m.rec.Source, r.m.rec.ArrivalRef, r.m.rec.Info.Size})
	}
	sort.SliceStable(ents, func(i, j int) bool {
		if ents[i].arrival != ents[j].arrival {
			return ents[i].arrival < ents[j].arrival
		}
		return ents[i].source < ents[j].source
	})
	out := make(map[string]int64, len(ents))
	var acc int64
	for _, e := range ents {
		out[e.source] = acc
		acc += e.size
	}
	return out
}

func median(v []int64) int64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]int64(nil), v...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[(len(s)-1)/2]
}

func percentile(v []int64, p int) int64 { return median(v) }

func emittedDelays(items []Item) []int64 {
	var out []int64
	for _, it := range items {
		if it.Decision == DecEmitted {
			out = append(out, it.DelayMs)
		}
	}
	return out
}

func ticksToUs(t int64) int64 {
	return t * 1000000 / timebase.TicksPerSecond
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// roundDiv returns round(a/b) with halves away from zero; b must be > 0.
func roundDiv(a, b int64) int64 {
	q := a / b
	r := a % b
	if 2*abs64(r) >= b {
		if a >= 0 {
			q++
		} else {
			q--
		}
	}
	return q
}
