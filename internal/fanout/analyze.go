// Package fanout implements unified-clock segmentation, multi-bitrate fan-out,
// independent per-representation retry hooks and fan-in boundary/keyframe
// validation. Analysis streams the TS once; segment byte ranges are addressed
// by absolute input offset so rebuilding one interval stays local.
package fanout

import (
	"io"

	"mediapack/internal/clock"
	merr "mediapack/internal/errors"
	"mediapack/internal/index"
	"mediapack/internal/ts"
)

// Interval is one unified-clock, keyframe-aligned packaging range.
type Interval struct {
	Index            int
	ID               string
	KeyframeOffset   int64
	StartOffset      int64
	EndOffset        int64
	StartTick        uint64
	EndTick          uint64
	StructuralDigest string
	MappingPaused    bool
	MappingRecovered bool
	Blocked          bool
	PauseReason      string
}

// Analysis is the result of streaming a TS input through every analyser.
type Analysis struct {
	Intervals       []Interval
	Issues          []merr.Issue
	Counts          map[merr.Category]int
	KeyIndex        *index.KeyframeIndex
	FullPackets     int
	FullPacketBytes int64
	MappingHistory  []ts.Mapping
	MappingGaps     []ts.MappingGap
	WrapCount       int
}

// Analyze streams rs from offset zero and builds intervals, the keyframe
// index, anomaly list and shared-clock associations. size is the input length.
func Analyze(rs io.ReadSeeker, size int64) (*Analysis, error) {
	if _, err := rs.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	book := merr.NewBook()
	rd := ts.NewReader(rs, 0, book)
	cc := ts.NewCCTracker()
	psi := ts.NewTracker(book)
	kidx := index.New()
	mediaClock := clock.NewMediaClock()

	var packets int
	keyTick := map[int64]uint64{}
	var lastPCR unwrappedPCR
	for {
		p, err := rd.Next()
		if err == ts.ErrTruncated {
			break
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		packets++

		if ev := cc.Observe(p); ev != nil {
			book.Add(ev.Issue())
		}
		kidx.AddPacket(p)

		if p.PCRFlag {
			tick, wrapped := mediaClock.FeedPCR(p.PCRBase)
			lastPCR = unwrappedPCR{tick: uint64(tick), have: true}
			if wrapped {
				book.Add(merr.Issue{Category: merr.CatPCRWrap, Severity: merr.SeverityWarning,
					Message: "PCR 33-bit wrap expanded onto monotonic clock", Offset: p.Offset,
					InputStart: p.Offset, InputEnd: p.Offset + ts.PacketSize, PacketIndex: p.Index,
					ClockTime: int64(tick), Recovery: merr.StateRecovered,
					Attrs: map[string]string{"raw_pcr_base": u64(p.PCRBase), "unwrapped": u64(uint64(tick))}})
			}
			if kidx.Exact(p.Offset) != nil {
				keyTick[p.Offset] = uint64(tick)
			}
		}
		if p.PES != nil && p.PES.HasPTS && lastPCR.have {
			pts := mediaClock.FeedPTS(p.PES.PTS)
			if _, ok := mediaClock.CheckAgainstPCR(pts, clock.Tick(clock.TickRate/10)); !ok {
				book.Add(merr.Issue{Category: merr.CatTimeBoundary, Message: "PTS diverges from shared PCR clock",
					Offset: p.Offset, InputStart: p.Offset, InputEnd: p.Offset + ts.PacketSize,
					PacketIndex: p.Index, ClockTime: int64(pts), Recovery: merr.StateOpen})
			}
		}

		psi.NowTick = uint64(mediaClock.Now())
		psi.Handle(p)
	}

	fullBytes := int64(packets) * ts.PacketSize
	a := &Analysis{KeyIndex: kidx, FullPackets: packets, FullPacketBytes: fullBytes,
		MappingHistory: psi.History, MappingGaps: psi.Gaps}
	a.WrapCount = book.Counts()[merr.CatPCRWrap]

	// Build intervals from consecutive keyframes, capped at full packet bytes.
	for i, k := range kidx.Keys {
		end := fullBytes
		if i+1 < len(kidx.Keys) {
			end = kidx.Keys[i+1].Offset
		}
		iv := Interval{Index: i, ID: segID(i), KeyframeOffset: k.Offset,
			StartOffset: k.Offset, EndOffset: end, StartTick: keyTick[k.Offset]}
		if i+1 < len(kidx.Keys) {
			iv.EndTick = keyTick[kidx.Keys[i+1].Offset]
		}
		a.Intervals = append(a.Intervals, iv)
	}
	// Last interval end tick: start + previous interval duration.
	if n := len(a.Intervals); n > 0 {
		last := &a.Intervals[n-1]
		if last.EndTick == 0 && n >= 2 {
			d := a.Intervals[n-1].StartTick - a.Intervals[n-2].StartTick
			last.EndTick = last.StartTick + d
		}
	}

	annotateMappingGaps(a.Intervals, psi)
	for i := range a.Intervals {
		iv := &a.Intervals[i]
		if iv.MappingPaused {
			state := merr.StatePaused
			if iv.MappingRecovered {
				state = merr.StateRecovered
			}
			book.Add(merr.Issue{Category: merr.CatMappingIncomplete, IntervalID: iv.ID,
				Message:    "packaging paused: incomplete PAT/PMT mapping (" + iv.PauseReason + ")",
				InputStart: iv.StartOffset, InputEnd: iv.EndOffset, Offset: iv.KeyframeOffset,
				ClockTime: int64(iv.StartTick), Recovery: state})
		}
	}

	// Associate CC anomalies with their containing keyframe interval.
	issues := book.All()
	for i := range issues {
		iss := &issues[i]
		if iss.Category != merr.CatCCJump && iss.Category != merr.CatCCDuplicate {
			continue
		}
		if kf := kidx.AtOrBefore(iss.Offset); kf != nil {
			iss.RelatedKeyframeOffset = kf.Offset
			if _, s, e := kidx.IntervalFor(iss.Offset, fullBytes); e > s {
				for _, iv := range a.Intervals {
					if iv.StartOffset == s && iv.EndOffset == e {
						iss.IntervalID = iv.ID
					}
				}
			}
		}
	}
	a.Issues = issues
	a.Counts = book.Counts()

	// Structural digest per interval via an independent range parse.
	for i := range a.Intervals {
		iv := &a.Intervals[i]
		length := iv.EndOffset - iv.StartOffset
		if _, err := rs.Seek(iv.StartOffset, io.SeekStart); err != nil {
			return nil, err
		}
		d, _, err := ts.DigestRange(rs, iv.StartOffset, length, nil)
		if err != nil {
			return nil, err
		}
		iv.StructuralDigest = d
	}
	return a, nil
}

type unwrappedPCR struct {
	tick uint64
	have bool
}

func annotateMappingGaps(intervals []Interval, psi *ts.Tracker) {
	// A closed gap pauses every interval it overlaps. An interval is only
	// considered recovered (packable) if a valid mapping returned before that
	// interval's end; otherwise it is held/blocked even if a later interval
	// eventually resolves the switch.
	for _, g := range psi.Gaps {
		for i := range intervals {
			iv := &intervals[i]
			if g.StartOffset >= iv.EndOffset || g.EndOffset <= iv.StartOffset {
				continue // no overlap
			}
			iv.MappingPaused = true
			iv.PauseReason = g.Reason
			if g.Resolved && g.EndOffset <= iv.EndOffset {
				iv.MappingRecovered = true
			} else {
				iv.Blocked = true
			}
		}
	}
	// A gap still open at end-of-stream blocks every interval from its start.
	if og := psi.OpenGap(); og != nil {
		for i := range intervals {
			iv := &intervals[i]
			if og.StartOffset >= iv.EndOffset {
				continue
			}
			iv.MappingPaused = true
			iv.PauseReason = og.Reason
			iv.Blocked = true
		}
	}
}

func segID(i int) string {
	return "seg_" + pad4(i)
}

func pad4(i int) string {
	s := ""
	if i < 10 {
		s = "000"
	} else if i < 100 {
		s = "00"
	} else if i < 1000 {
		s = "0"
	}
	return s + itoa(i)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	k := len(b)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		k--
		b[k] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		k--
		b[k] = '-'
	}
	return string(b[k:])
}

func u64(v uint64) string { return itoa64(v) }

func itoa64(n uint64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	k := len(b)
	for n > 0 {
		k--
		b[k] = byte('0' + n%10)
		n /= 10
	}
	return string(b[k:])
}
