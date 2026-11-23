package inspect

import (
	"sort"

	"archivecheck/internal/manifest"
	"archivecheck/internal/model"
	"archivecheck/internal/transport"
)

// Confidence states how strongly a damage range is supported by evidence.
type Confidence string

const (
	// ConfidenceExact means both edges of the gap are anchored to delivered
	// packets whose timestamps map cleanly onto the shared timeline.
	ConfidenceExact Confidence = "exact"
	// ConfidenceEstimated means the edges were derived from segment bounds or
	// non-monotonic timestamps rather than both neighbouring packets.
	ConfidenceEstimated Confidence = "estimated"
	// ConfidenceUnknown means no timestamp mapping was available at all.
	ConfidenceUnknown Confidence = "unknown"
)

// DamageRange is a media-time interval known to be damaged.
type DamageRange struct {
	// Start and End are plan ticks; the interval is [Start,End).
	Start int64 `json:"start"`
	End   int64 `json:"end"`
	// Confidence records how firmly the edges are supported.
	Confidence Confidence `json:"confidence"`
	// Representations lists the representations the range applies to.
	Representations []string `json:"representations"`
	// Segments lists the manifest segment identifiers that overlap the range.
	Segments []string `json:"segments"`
	// MissingPackets is the transport packet count attributed to the range.
	MissingPackets uint64 `json:"missing_packets"`
	// Causes lists the fault codes that produced the range.
	Causes []model.Code `json:"causes"`
	// Timebase is the shared timebase of the interval.
	Timebase int64 `json:"timebase"`
}

// Wider returns a range covering both.
func (d DamageRange) Wider(o DamageRange) DamageRange {
	out := d
	if o.Start < out.Start {
		out.Start = o.Start
	}
	if o.End > out.End {
		out.End = o.End
	}
	out.Representations = unionStrings(d.Representations, o.Representations)
	out.Segments = unionStrings(d.Segments, o.Segments)
	out.Causes = unionCodes(d.Causes, o.Causes)
	out.MissingPackets += o.MissingPackets
	out.Confidence = weaker(d.Confidence, o.Confidence)
	return out
}

// rtpToMedia converts an RTP timestamp into plan ticks. The anchor pairs one
// delivered packet's RTP timestamp with its media time; only the difference is
// used, so anchor drift cannot inflate the result.
func rtpToMedia(ts uint32, anchorRTP uint32, anchorMedia, rtpTimebase, planTimebase int64) (int64, bool) {
	if rtpTimebase <= 0 || planTimebase <= 0 {
		return 0, false
	}
	delta := int64(int32(ts - anchorRTP))
	if delta < 0 {
		return 0, false
	}
	v, err := model.ConvertTicks(delta, rtpTimebase, planTimebase)
	if err != nil {
		return 0, false
	}
	return anchorMedia + v.Ticks, true
}

// GapAnchor ties a segment's RTP clock to the manifest timeline.
type GapAnchor struct {
	// RTPTimestamp is the timestamp of the first delivered packet in the
	// segment.
	RTPTimestamp uint32
	// Media is the plan tick where the segment starts.
	Media int64
	// RTPTimebase is the RTP clock rate in Hz.
	RTPTimebase int64
}

// MapGap translates one transport gap into a media-time damage range.
//
// The damaged interval is [last delivered timestamp, first delivered
// timestamp after the gap). When either edge cannot be anchored it falls back
// to the segment bounds and marks the result estimated.
func MapGap(seg manifest.Segment, representation, file string, gap transport.Gap, anchor GapAnchor, planTimebase int64) (DamageRange, *model.Fault) {
	dr := DamageRange{
		Start:           seg.Start,
		End:             seg.End(),
		Representations: []string{representation},
		Segments:        []string{seg.ID},
		MissingPackets:  gap.Count,
		Causes:          []model.Code{model.CodeTransportGap},
		Timebase:        planTimebase,
		Confidence:      ConfidenceEstimated,
	}
	if gap.Count == 0 {
		dr.End = dr.Start
		dr.Confidence = ConfidenceUnknown
		return dr, nil
	}
	start, okStart := rtpToMedia(gap.FirstTimestamp, anchor.RTPTimestamp, anchor.Media, anchor.RTPTimebase, planTimebase)
	end, okEnd := rtpToMedia(gap.LastTimestamp, anchor.RTPTimestamp, anchor.Media, anchor.RTPTimebase, planTimebase)
	switch {
	case okStart && okEnd && end > start:
		dr.Start, dr.End = start, end
		dr.Confidence = ConfidenceExact
	case okStart && okEnd && end == start:
		// Both edges share a timestamp: the gap fits inside one frame. Widen
		// it by a single tick so the range is never empty.
		dr.Start, dr.End = start, start+1
		dr.Confidence = ConfidenceEstimated
	default:
		dr.Confidence = ConfidenceUnknown
	}
	// Clamp to the declared segment so a bad anchor cannot leak damage into a
	// healthy neighbour.
	if dr.Start < seg.Start {
		dr.Start = seg.Start
	}
	if dr.End > seg.End() {
		dr.End = seg.End()
	}
	if dr.End < dr.Start {
		dr.Start, dr.End = seg.Start, seg.End()
		dr.Confidence = ConfidenceEstimated
	}
	fault := model.NewFault(model.CodeDamageGap, model.SeverityError, model.Source{
		Layer: "inspect", Segment: seg.ID, Representation: representation, File: file, Offset: -1,
	}, "transport gap of %d packets maps to %s confidence damage", gap.Count, dr.Confidence).
		WithMedia(dr.Start, dr.End, planTimebase)
	return dr, fault
}

// MergeOptions tunes damage-range merging.
type MergeOptions struct {
	// FrameDuration is the target frame duration in plan ticks. Ranges whose
	// separation is at most this many ticks are merged; wider separations stay
	// distinct so a clean stretch is not reported as damaged.
	FrameDuration int64
	// Timebase is the shared timebase.
	Timebase int64
}

// MergeDamage sorts ranges and merges those separated by at most one target
// frame duration. This joins a gap that straddles a segment boundary while
// keeping genuinely separate outages apart.
func MergeDamage(ranges []DamageRange, opts MergeOptions) []DamageRange {
	if len(ranges) == 0 {
		return nil
	}
	sorted := append([]DamageRange(nil), ranges...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Start != sorted[j].Start {
			return sorted[i].Start < sorted[j].Start
		}
		return sorted[i].End < sorted[j].End
	})
	frame := opts.FrameDuration
	if frame < 0 {
		frame = 0
	}
	out := []DamageRange{sorted[0]}
	for _, r := range sorted[1:] {
		last := &out[len(out)-1]
		if r.Start <= last.End+frame {
			*last = last.Wider(r)
			continue
		}
		out = append(out, r)
	}
	return out
}

// DamageFaults returns one fault per damaged range.
func DamageFaults(ranges []DamageRange, fileFor func(seg string) string) []*model.Fault {
	var faults []*model.Fault
	for _, r := range ranges {
		seg := "-"
		if len(r.Segments) > 0 {
			seg = r.Segments[0]
		}
		file := ""
		if fileFor != nil {
			file = fileFor(seg)
		}
		faults = append(faults, model.NewFault(model.CodeDamageGap, model.SeverityError, model.Source{
			Layer: "inspect", Segment: seg, File: file, Offset: -1,
		}, "damaged interval with %d missing packets, %s confidence", r.MissingPackets, r.Confidence).
			WithMedia(r.Start, r.End, r.Timebase))
	}
	return faults
}

func weaker(a, b Confidence) Confidence {
	rank := func(c Confidence) int {
		switch c {
		case ConfidenceExact:
			return 2
		case ConfidenceEstimated:
			return 1
		default:
			return 0
		}
	}
	if rank(a) <= rank(b) {
		return a
	}
	return b
}

func unionStrings(a, b []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(a)+len(b))
	for _, list := range [][]string{a, b} {
		for _, s := range list {
			if s == "" || seen[s] {
				continue
			}
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func unionCodes(a, b []model.Code) []model.Code {
	seen := map[model.Code]bool{}
	out := make([]model.Code, 0, len(a)+len(b))
	for _, list := range [][]model.Code{a, b} {
		for _, c := range list {
			if seen[c] {
				continue
			}
			seen[c] = true
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
