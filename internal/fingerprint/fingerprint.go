package fingerprint

import (
	"mediavault/internal/timeline"
)

// SampleFingerprint joins the fragment-byte hash and the decoded-frame
// summary for one sample, plus the timeline alignment residual in ns.
type SampleFingerprint struct {
	Track       string  `json:"track"`
	Order       uint64  `json:"order"`
	FragmentSHA string  `json:"fragment_sha256"`
	FrameSHA    string  `json:"frame_sha256"`
	PixelMean   float64 `json:"pixel_mean,omitempty"`
	PixelVar    float64 `json:"pixel_variance,omitempty"`
	PeakDBFS    float64 `json:"peak_dbfs,omitempty"`
	Silent      bool    `json:"silent,omitempty"`
	AlignmentNs int64   `json:"alignment_error_ns"`
	DTSNs       int64   `json:"dts_ns"`
}

// SegmentFingerprint aggregates the per-sample fingerprints of one segment
// and records whether two segments share content but sit at different
// timeline positions (content duplicate vs timeline-distinct).
type SegmentFingerprint struct {
	Track         string             `json:"track"`
	StartOrder    uint64             `json:"start_order"`
	EndOrder      uint64             `json:"end_order"`
	SegmentSHA    string             `json:"segment_sha256"`
	Samples       []SampleFingerprint `json:"samples"`
	ContentKey    string             `json:"content_key"`
	TimelineKey   string             `json:"timeline_key"`
	ContentDupe   bool               `json:"content_duplicate"`
	TimelineDistinct bool            `json:"timeline_distinct"`
}

// OfSample fingerprints one normalized sample. The alignment residual is
// the difference between the exact rational DTS and its integer-ns rendering,
// which for well-formed material stays well below 1 ms.
func OfSample(s timeline.Sample) SampleFingerprint {
	dtsNs := s.TS.DTS.Nanoseconds()
	exactNs := s.TS.DTS
	alignment := exactNs.Sub(nsToRational(dtsNs)).Abs().Nanoseconds()
	fp := SampleFingerprint{
		Track:       s.Track.String(),
		Order:       s.Order,
		FragmentSHA: FragmentHash(s.Payload),
		AlignmentNs: alignment,
		DTSNs:       dtsNs,
	}
	switch s.Track.Kind {
	case timeline.KindVideo:
		st := VideoFrame(s.Payload, alignment)
		fp.FrameSHA = st.FrameHash
		fp.PixelMean = st.Mean
		fp.PixelVar = st.Variance
	case timeline.KindAudio:
		au := AudioFrame(s.Payload)
		fp.FrameSHA = au.FrameHash
		fp.PeakDBFS = au.PeakDBFS
		fp.Silent = IsSilence(au)
	default:
		fp.FrameSHA = FrameHash(s.Payload)
	}
	return fp
}

// OfSegment folds a run of samples into one segment fingerprint. The
// content key depends only on fragment+frame hashes; the timeline key adds
// the DTS positions, so identical content at different times is recognized
// as a content duplicate that is nonetheless timeline-distinct.
func OfSegment(track string, samples []timeline.Sample) SegmentFingerprint {
	fps := make([]SampleFingerprint, 0, len(samples))
	contentInput := make([]byte, 0, len(samples)*64)
	timelineInput := make([]byte, 0, len(samples)*72)
	var start, end uint64
	for i, s := range samples {
		fp := OfSample(s)
		fps = append(fps, fp)
		contentInput = append(contentInput, fp.FragmentSHA...)
		contentInput = append(contentInput, fp.FrameSHA...)
		// Timeline input extends the content input with the exact DTS,
		// so equal content at different positions gets a distinct key.
		timelineInput = append(timelineInput, fp.FragmentSHA...)
		timelineInput = append(timelineInput, fp.FrameSHA...)
		timelineInput = append(timelineInput, itoaBytes(fp.DTSNs)...)
		if i == 0 {
			start = s.Order
		}
		end = s.Order
	}
	return SegmentFingerprint{
		Track:       track,
		StartOrder:  start,
		EndOrder:    end,
		SegmentSHA:  FragmentHash(contentInput),
		Samples:     fps,
		ContentKey:  StableKey("content", FragmentHash(contentInput), track),
		TimelineKey: StableKey("timeline", FragmentHash(timelineInput), track),
	}
}

// MarkDuplicate compares a segment against a previously archived content
// key. It flags content duplicates and, when the timeline keys differ,
// marks the segment timeline-distinct rather than identical.
func MarkDuplicate(seg *SegmentFingerprint, knownContent, knownTimeline map[string]bool) {
	if knownContent[seg.ContentKey] {
		seg.ContentDupe = true
	}
	if seg.ContentDupe && !knownTimeline[seg.TimelineKey] {
		seg.TimelineDistinct = true
	}
}

func nsToRational(ns int64) timeline.Rational {
	return timeline.Rational{Num: ns, Den: 1_000_000_000}.Normalize()
}

// itoaBytes renders an int64 as its decimal bytes without pulling in fmt.
func itoaBytes(v int64) []byte {
	if v == 0 {
		return []byte{'0'}
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return buf[i:]
}
