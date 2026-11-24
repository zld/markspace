// Package syncer estimates and corrects audio/video skew on the shared
// clock. A single global offset is estimated from the first samples;
// per-segment residuals above the configured threshold are flagged as
// desync segments. Corrections stay local to their segment and never
// propagate into whole-file time drift.
package syncer

import (
	"mediacheck/internal/adapter"
	"mediacheck/internal/fault"
)

// Segment is one alignment window over the media timeline.
type Segment struct {
	Index        int
	StartNanos   int64
	EndNanos     int64
	AVDeltaNanos int64 // residual video-minus-audio skew, global offset removed
	Desync       bool
}

// Align compares the video and audio tracks on the shared nanosecond
// clock. segNanos is the analysis window; |residual| above thresholdNanos
// marks the segment as desynchronized (recorded as AV_DESYNC faults).
func Align(v, a *adapter.Track, segNanos, thresholdNanos int64, fl *fault.List) []Segment {
	if v == nil || a == nil || len(v.Samples) == 0 || len(a.Samples) == 0 || segNanos <= 0 {
		return nil
	}
	vNs := make([]int64, len(v.Samples))
	for i, s := range v.Samples {
		vNs[i] = v.Timescale.ToNanos(s.CTS)
	}
	aNs := make([]int64, len(a.Samples))
	for i, s := range a.Samples {
		aNs[i] = a.Timescale.ToNanos(s.CTS)
	}
	// Global offset from the first samples only: a constant, never
	// re-estimated downstream, so local fixes cannot drift the whole file.
	offset := vNs[0] - aNs[0]

	end := vNs[len(vNs)-1]
	if aNs[len(aNs)-1] > end {
		end = aNs[len(aNs)-1]
	}
	var segs []Segment
	ai := 0
	for start, idx := int64(0), 0; start <= end; start, idx = start+segNanos, idx+1 {
		seg := Segment{Index: idx, StartNanos: start, EndNanos: start + segNanos}
		var sum int64
		var cnt int
		for _, vn := range vNs {
			if vn < start || vn >= start+segNanos {
				continue
			}
			for ai+1 < len(aNs) && aNs[ai+1] <= vn {
				ai++
			}
			sum += vn - aNs[ai] - offset
			cnt++
		}
		if cnt > 0 {
			seg.AVDeltaNanos = sum / int64(cnt)
		}
		d := seg.AVDeltaNanos
		if d < 0 {
			d = -d
		}
		if d > thresholdNanos {
			seg.Desync = true
			fl.Add(fault.ErrDesync, -1,
				"segment %d [%dms,%dms): av skew %d ns exceeds threshold %d ns",
				idx, start/1e6, (start+segNanos)/1e6, seg.AVDeltaNanos, thresholdNanos)
		}
		segs = append(segs, seg)
	}
	return segs
}
