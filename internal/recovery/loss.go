// Package recovery classifies sequence gaps into contiguous packet losses and
// decides which gaps can be reconstructed from surviving data. Recovery is
// strictly declared: an unrecoverable gap yields an affected time interval and
// never fabricates media.
package recovery

import (
	"sort"

	"mediasync/internal/model"
)

// Kind classifies a loss gap.
type Kind string

const (
	// Contiguous is a run of missing sequence numbers with no evidence of
	// how to fill it.
	Contiguous Kind = "contiguous"
	// VideoKeyframeDependency is a video gap inside a GOP: decoding can
	// resume only at the next keyframe, so the affected interval extends to
	// that keyframe.
	VideoKeyframeDependency Kind = "video_keyframe_dependency"
	// AudioInterval is an audio gap whose affected span is estimated from the
	// surrounding audio cadence. The estimate bounds the interval; no samples
	// are synthesized.
	AudioInterval Kind = "audio_interval"
)

// Gap is one maximal run of missing packets in the extended sequence space.
type Gap struct {
	// After is the last present extended sequence before the gap.
	After int64
	// Before is the first present extended sequence after the gap, or -1 when
	// the gap runs to the end of input.
	Before int64
	// Count is Before-After-1, or the redundancy-derived repair count when
	// Before is -1.
	Count int64
	Kind  Kind

	// Start and End bound the affected media time on the kernel base. End is
	// exclusive.
	Start model.Nanos
	End   model.Nanos

	// Recoverable is true only when the kernel can fill the gap without
	// inventing media: enough redundant copies were observed.
	Recoverable bool
	// Redundant is the number of missing packets supplied by redundancy.
	Redundant int64
	// SourceSeq lists the packet ext that carried each redundant copy, in
	// ascending order.
	SourceSeq []int64
}

// Packet is the minimum the classifier needs to know about a present packet.
type Packet struct {
	Ext       int64
	TrackID   uint32
	Kind      model.TrackKind
	Keyframe  bool
	MediaTime model.Nanos
	// End is the packet's media end, used for audio interval estimation.
	End model.Nanos
	// Redundant marks a packet that carries copies of previously lost
	// packets.
	Redundant bool
	// Carries lists extended sequence numbers this packet redundantly
	// carries.
	Carries []int64
}

// Input is the evidence handed to Classify for one track.
type Input struct {
	TrackID uint32
	Kind    model.TrackKind
	Packets []Packet
	// AudioCadence is the nominal inter-packet media duration for audio
	// tracks. Zero disables audio interval estimation.
	AudioCadence model.Nanos
	// KeyframeTimes lists keyframe media times, ascending, used to extend a
	// video gap to its recovery point.
	KeyframeTimes []model.Nanos
}

// Report is the classification result for one track.
type Report struct {
	TrackID       uint32
	Gaps          []Gap
	Lost          int64
	Recovered     int64
	Unrecoverable int64
}

// Classify finds gaps between the first and last observed extended sequence
// numbers and classifies each one. Deriving the observed bounds from the
// packets means a hole at either end is reported as an edge gap only to the
// extent redundancy proves it existed.
func Classify(in Input) Report {
	rep := Report{TrackID: in.TrackID}
	if len(in.Packets) == 0 {
		return rep
	}
	pkts := append([]Packet(nil), in.Packets...)
	sort.SliceStable(pkts, func(i, j int) bool { return pkts[i].Ext < pkts[j].Ext })
	last := pkts[len(pkts)-1].Ext

	// Build a redundancy map: missing ext -> carrying packet ext.
	redundant := map[int64]int64{}
	for _, p := range pkts {
		for _, c := range p.Carries {
			if _, exists := redundant[c]; !exists {
				redundant[c] = p.Ext
			}
		}
	}

	covered := map[int64]bool{}
	prev := pkts[0]
	for _, p := range pkts[1:] {
		if p.Ext <= prev.Ext {
			continue
		}
		if p.Ext-prev.Ext > 1 {
			g := buildGap(prev, p, in, redundant)
			rep.Gaps = append(rep.Gaps, g)
			rep.Lost += g.Count
			rep.Recovered += g.Redundant
			if !g.Recoverable {
				rep.Unrecoverable += g.Count - g.Redundant
			}
			for k := range redundant {
				if k > prev.Ext && k < p.Ext {
					covered[k] = true
				}
			}
		}
		prev = p
	}

	// A trailing gap exists only if redundancy entries name sequence numbers
	// beyond the last present packet: those packets were sent and lost. The
	// trailing hole therefore always ends unknown and is unrecoverable; the
	// redundant copies only supply the exact missing count.
	edge := int64(-1)
	for k := range redundant {
		if k > last && (edge == -1 || k > edge) {
			edge = k
		}
	}
	if edge > last {
		g := Gap{After: last, Before: -1, Kind: edgeKind(in), Start: lastPacketEnd(pkts[len(pkts)-1])}
		var src []int64
		for k := last + 1; k <= edge; k++ {
			g.Count++
			if s, ok := redundant[k]; ok {
				g.Redundant++
				src = append(src, s)
			}
		}
		sort.Slice(src, func(i, j int) bool { return src[i] < src[j] })
		g.SourceSeq = src
		span := cadenceOr(in.AudioCadence, model.Nanos(model.NanosPerMilli*20))
		g.End = g.Start + model.Nanos(g.Count)*span
		// Nothing follows the hole, so the recovery point is unknown.
		g.Recoverable = false
		rep.Gaps = append(rep.Gaps, g)
		rep.Lost += g.Count
		rep.Recovered += g.Redundant
		rep.Unrecoverable += g.Count - g.Redundant
	}
	sort.SliceStable(rep.Gaps, func(i, j int) bool { return rep.Gaps[i].After < rep.Gaps[j].After })
	return rep
}

func buildGap(a, b Packet, in Input, redundant map[int64]int64) Gap {
	g := Gap{After: a.Ext, Before: b.Ext, Kind: edgeKind(in)}
	g.Start = a.End
	g.End = b.MediaTime
	if g.End <= g.Start {
		g.End = g.Start
	}
	var src []int64
	for k := a.Ext + 1; k < b.Ext; k++ {
		if s, ok := redundant[k]; ok {
			g.Redundant++
			src = append(src, s)
		} else {
			g.Count++
		}
	}
	g.Count += g.Redundant
	g.Recoverable = g.Redundant == g.Count && g.Count > 0
	sort.Slice(src, func(i, j int) bool { return src[i] < src[j] })
	g.SourceSeq = src

	switch in.Kind {
	case model.KindVideo:
		g.Kind = VideoKeyframeDependency
		// Extend the affected interval to the next keyframe at or after the
		// following packet: decoding cannot resume before it.
		if kf, ok := nextKeyframe(in.KeyframeTimes, b.MediaTime); ok {
			g.End = kf
		} else if b.Keyframe {
			g.End = b.MediaTime
		}
	case model.KindAudio:
		g.Kind = AudioInterval
		if in.AudioCadence > 0 {
			// Bound the interval with the estimated missing span rather than
			// trusting a single neighbour timestamp.
			estEnd := a.End + model.Nanos(g.Count)*in.AudioCadence
			if estEnd > g.Start && estEnd < b.MediaTime {
				g.End = estEnd
			}
		}
	default:
		g.Kind = Contiguous
	}
	return g
}

func edgeKind(in Input) Kind {
	switch in.Kind {
	case model.KindVideo:
		return VideoKeyframeDependency
	case model.KindAudio:
		return AudioInterval
	default:
		return Contiguous
	}
}

func nextKeyframe(times []model.Nanos, at model.Nanos) (model.Nanos, bool) {
	for _, t := range times {
		if t >= at {
			return t, true
		}
	}
	return 0, false
}

func lastPacketEnd(p Packet) model.Nanos {
	if p.End > p.MediaTime {
		return p.End
	}
	return p.MediaTime
}

func cadenceOr(c, def model.Nanos) model.Nanos {
	if c > 0 {
		return c
	}
	return def
}

// Intervals returns the affected media intervals for every gap, sorted and
// merged, which is what the timeline marks as unrecoverable.
func (r Report) Intervals() []model.Interval {
	out := make([]model.Interval, 0, len(r.Gaps))
	for _, g := range r.Gaps {
		if g.End > g.Start {
			out = append(out, model.Interval{Start: g.Start, End: g.End})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Start != out[j].Start {
			return out[i].Start < out[j].Start
		}
		return out[i].End < out[j].End
	})
	merged := out[:0]
	for _, iv := range out {
		if n := len(merged); n > 0 && iv.Start <= merged[n-1].End {
			if iv.End > merged[n-1].End {
				merged[n-1].End = iv.End
			}
			continue
		}
		merged = append(merged, iv)
	}
	return merged
}
