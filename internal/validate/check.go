// Package validate applies cross-module rules: cue-to-keyframe mapping,
// subtitle/media clock drift, out-of-range cues, late-event accounting and
// damaged-region aggregation. It emits the shared error model and never
// rewrites original timestamps.
package validate

import (
	"fmt"
	"sort"

	"mediasync/internal/clock"
	"mediasync/internal/issue"
	"mediasync/internal/stream"
	"mediasync/internal/subtitle"
	"mediasync/internal/timeline"
)

// CueMap is the mapping of one subtitle cue onto media structure.
type CueMap struct {
	CueIndex       int
	Line           int
	CueStart       clock.Tick
	CueEnd         clock.Tick
	Keyframe       int // 0 when the cue precedes the first keyframe
	KeyframePTS    clock.Tick
	KeyframeOffset int64
	TimeDev        clock.Tick // absolute ticks from cue start to mapped keyframe
	Packet         int
	PacketTime     clock.Tick
	PacketDev      clock.Tick
	ByteDev        int64
}

// KeyframeStat is the interval summary derived from actual decode time.
type KeyframeStat struct {
	Count  int
	Min    clock.Tick
	Median clock.Tick
	Max    clock.Tick
}

// Result is the full analysis of one sample.
type Result struct {
	Name           string
	Window         clock.Tick
	Sources        []string
	Events         []timeline.Event
	Gaps           []timeline.Gap
	CueMaps        []CueMap
	Intervals      []stream.Interval
	Keyframes      KeyframeStat
	MaxTimeDev     clock.Tick
	MaxByteDev     int64
	Damaged        []issue.ByteRange
	Issues         issue.List
	Drift          clock.Correction
	DriftKnown     bool
	VFR            bool
	VFRReason      string
	PacketCount    int
	RTPWraps       int
	RTPDuplicates  int
	RTPRegressions int
	RTPMissing     uint64
	// Notes carries deterministic, human-readable facts derived from the
	// inputs (for example legal timecode conversions). It never includes
	// wall-clock data.
	Notes                                       []string
	Admitted, Recomputed, Unrecoverable, Sealed int
	Replays                                     []timeline.Replay
	First                                       *issue.Issue
}

// Spec is the input to Analyze.
type Spec struct {
	Name      string
	Window    clock.Tick
	Cues      []subtitle.Track
	CueIssues issue.List
	MP4       *stream.MP4Result
	RTP       *stream.RTPSession
	// Arrival reorders the canonical events to simulate late delivery.
	// When nil, canonical event-time order is used.
	Arrival func([]timeline.Event) []timeline.Event
}

// Analyze runs the timeline engine and all cross-module rules.
func Analyze(spec Spec) *Result {
	res := &Result{Name: spec.Name, Window: spec.Window}
	if spec.MP4 != nil {
		res.Sources = append(res.Sources, spec.MP4.Input)
		res.Issues.Add(spec.MP4.Issues...)
		res.Damaged = append(res.Damaged, spec.MP4.Damaged...)
		res.Intervals = spec.MP4.Intervals
		res.VFR, res.VFRReason = spec.MP4.VFR, spec.MP4.VFRReason
		res.Keyframes = KeyframeStat{
			Count: len(spec.MP4.Keyframes), Min: spec.MP4.MinGap,
			Median: spec.MP4.MedianGap, Max: spec.MP4.MaxGap,
		}
	}
	if spec.RTP != nil {
		res.Sources = append(res.Sources, spec.RTP.Input)
		res.Issues.Add(spec.RTP.Issues...)
		res.Damaged = append(res.Damaged, spec.RTP.Damaged...)
		res.PacketCount = len(spec.RTP.Packets)
		res.RTPWraps = spec.RTP.Wraps
		res.RTPDuplicates = spec.RTP.Duplicates
		res.RTPRegressions = spec.RTP.Regressions
		res.RTPMissing = spec.RTP.Missing
	}
	for _, tr := range spec.Cues {
		res.Sources = append(res.Sources, tr.Input)
	}
	res.Issues.Add(spec.CueIssues...)

	events, cueLine := buildEvents(spec)
	if spec.Arrival != nil {
		events = spec.Arrival(events)
	}
	tl := timeline.New(spec.Window)
	for _, ev := range events {
		tl.Push(ev)
	}
	res.Events = tl.Events()
	res.Gaps = tl.Gaps()
	res.Issues.Add(tl.Issues()...)
	res.Admitted, res.Recomputed, res.Unrecoverable, res.Sealed = tl.Counters()
	res.Replays = tl.Replays()

	if spec.MP4 != nil && len(spec.MP4.Keyframes) > 0 {
		res.CueMaps = mapCues(spec.Cues, spec.MP4, spec.RTP, cueLine, &res.Issues)
		for _, m := range res.CueMaps {
			if m.TimeDev > res.MaxTimeDev {
				res.MaxTimeDev = m.TimeDev
			}
			if m.ByteDev > res.MaxByteDev {
				res.MaxByteDev = m.ByteDev
			}
		}
		res.Drift, res.DriftKnown = estimateDrift(res.CueMaps)
		if res.DriftKnown && res.Drift.Exceeded {
			it := issue.New(issue.DriftExceeded, firstSource(spec),
				fmt.Sprintf("clock drift %s exceeds bound %s at %s; original timestamps kept",
					res.Drift.Measured, res.Drift.Limit, res.Drift.At))
			res.Issues.Add(it)
		}
	}
	checkCueRange(spec.Cues, spec.MP4, &res.Issues)
	res.Damaged = issue.MergeRanges(res.Damaged, 4)
	res.Issues = res.Issues.Sorted()
	if first, ok := res.Issues.First(); ok {
		f := first
		res.First = &f
	}
	return res
}

// buildEvents converts parsed modules into timeline events. cueLine maps the
// generated cue event identities back to the parsed cue for reporting.
func buildEvents(spec Spec) ([]timeline.Event, map[string]subtitle.Cue) {
	cueLine := map[string]subtitle.Cue{}
	var events []timeline.Event
	for _, tr := range spec.Cues {
		for _, c := range tr.Cues {
			ref := fmt.Sprintf("cue:%d", c.Index)
			events = append(events, timeline.Event{
				Kind: timeline.KindCue, Time: c.Start, End: c.End,
				Input: tr.Input, Line: c.EndLn, Offset: -1, Ref: ref,
			})
			cueLine[tr.Input+"|"+ref] = c
		}
	}
	if spec.MP4 != nil {
		for _, k := range spec.MP4.Keyframes {
			events = append(events, timeline.Event{
				Kind: timeline.KindKeyframe, Time: k.DTS, End: k.DTS,
				Input: spec.MP4.Input, Line: 0, Offset: k.Offset,
				Packet: k.Index, Ref: fmt.Sprintf("kf:%d", k.Index),
			})
		}
	}
	if spec.RTP != nil {
		for _, p := range spec.RTP.Packets {
			events = append(events, timeline.Event{
				Kind: timeline.KindPacket, Time: p.Arrival, End: p.Arrival,
				Input: spec.RTP.Input, Line: 0, Offset: p.Offset,
				Packet: p.Index, Ref: fmt.Sprintf("rtp:%d", p.Index),
			})
		}
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].Less(events[j]) })
	return events, cueLine
}

func mapCues(tracks []subtitle.Track, mp4 *stream.MP4Result, rtp *stream.RTPSession, cueLine map[string]subtitle.Cue, issues *issue.List) []CueMap {
	first := mp4.Keyframes[0]
	last := mp4.Keyframes[len(mp4.Keyframes)-1]
	spanTicks := last.DTS - first.DTS
	spanBytes := last.Offset - first.Offset
	var out []CueMap
	for _, tr := range tracks {
		for _, c := range tr.Cues {
			m := CueMap{CueIndex: c.Index, Line: c.StartLn, CueStart: c.Start, CueEnd: c.End}
			var best *stream.Keyframe
			for i := range mp4.Keyframes {
				k := &mp4.Keyframes[i]
				if k.PTS > c.Start {
					break
				}
				best = k
			}
			if best == nil {
				best = &mp4.Keyframes[0]
			}
			m.Keyframe = best.Index
			m.KeyframePTS = best.PTS
			m.KeyframeOffset = best.Offset
			m.TimeDev = abs(c.Start - best.PTS)
			if spanTicks > 0 && spanBytes > 0 {
				cbr := first.Offset + int64(int64(best.DTS-first.DTS)*spanBytes/int64(spanTicks))
				m.ByteDev = absInt64(best.Offset - cbr)
			}
			if rtp != nil && len(rtp.Packets) > 0 {
				p := nearestPacket(rtp.Packets, c.Start)
				m.Packet = p.Index
				m.PacketTime = p.Arrival
				m.PacketDev = abs(c.Start - p.Arrival)
			}
			out = append(out, m)
		}
	}
	_ = cueLine
	return out
}

func nearestPacket(pk []stream.RTPPacket, t clock.Tick) stream.RTPPacket {
	i := sort.Search(len(pk), func(i int) bool { return pk[i].Arrival >= t })
	switch {
	case i == 0:
		return pk[0]
	case i >= len(pk):
		return pk[len(pk)-1]
	}
	if abs(pk[i].Arrival-t) < abs(t-pk[i-1].Arrival) {
		return pk[i]
	}
	return pk[i-1]
}

func estimateDrift(maps []CueMap) (clock.Correction, bool) {
	var est clock.DriftEstimator
	for _, m := range maps {
		est.Add(m.CueStart, m.KeyframePTS)
	}
	c, ok := est.Analyze()
	return c, ok
}

func checkCueRange(tracks []subtitle.Track, mp4 *stream.MP4Result, issues *issue.List) {
	if mp4 == nil || mp4.Track.Duration <= 0 {
		return
	}
	for _, tr := range tracks {
		for _, c := range tr.Cues {
			if c.End > mp4.Track.Duration {
				issues.Add(issue.New(issue.CueOutOfRange, tr.Input,
					fmt.Sprintf("cue %d ends %s after media duration %s", c.Index, c.End, mp4.Track.Duration)).
					WithLine(c.EndLn).WithRef(fmt.Sprintf("cue:%d", c.Index)))
			}
		}
	}
}

func firstSource(spec Spec) string {
	if spec.MP4 != nil {
		return spec.MP4.Input
	}
	if spec.RTP != nil {
		return spec.RTP.Input
	}
	if len(spec.Cues) > 0 {
		return spec.Cues[0].Input
	}
	return spec.Name
}

func abs(t clock.Tick) clock.Tick {
	if t < 0 {
		return -t
	}
	return t
}

func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
