package rewrap

import (
	"sort"

	"mediapipeline/pkg/media"
)

// Verification contains exact source/output preservation counts.
type Verification struct {
	Tracks            int     `json:"tracks"`
	Samples           int     `json:"samples"`
	FullSampleMatch   int     `json:"full_sample_match"`
	ByteMatch         int     `json:"byte_match"`
	TimestampMatch    int     `json:"timestamp_match"`
	SyncMatch         int     `json:"sync_match"`
	KeyframesExpected int     `json:"keyframes_expected"`
	KeyframesMatched  int     `json:"keyframes_matched"`
	Segments          int     `json:"segments"`
	SegmentsMatched   int     `json:"segments_matched"`
	RoundtripRate     float64 `json:"roundtrip_rate"`
	KeyframeHitRate   float64 `json:"keyframe_hit_rate"`
	SegmentHashRate   float64 `json:"segment_hash_rate"`
}

// Compare reparsed output against the original parsed metadata.
func Compare(src, out *media.ContainerInfo) Verification {
	var v Verification
	outTracks := make(map[uint32]*media.TrackInfo, len(out.Tracks))
	for i := range out.Tracks {
		outTracks[out.Tracks[i].TrackID] = &out.Tracks[i]
	}

	for _, st := range src.Tracks {
		ot := outTracks[st.TrackID]
		if ot == nil {
			continue
		}
		v.Tracks++
		n := len(st.Samples)
		if len(ot.Samples) < n {
			n = len(ot.Samples)
		}
		for i := 0; i < n; i++ {
			a, b := st.Samples[i], ot.Samples[i]
			v.Samples++
			bytesOK := a.SHA256 != "" && a.SHA256 == b.SHA256
			tsOK := a.DTS == b.DTS && a.PTS == b.PTS && a.Duration == b.Duration
			syncOK := a.Sync == b.Sync
			if bytesOK {
				v.ByteMatch++
			}
			if tsOK {
				v.TimestampMatch++
			}
			if syncOK {
				v.SyncMatch++
			}
			if bytesOK && tsOK && syncOK && a.Number == b.Number {
				v.FullSampleMatch++
			}
		}

		expectedKeys := keyframeNumbers(st)
		actualKeys := keyframeNumbers(*ot)
		v.KeyframesExpected += len(expectedKeys)
		for n := range expectedKeys {
			if actualKeys[n] {
				v.KeyframesMatched++
			}
		}

		actualSeg := make(map[int]media.Segment, len(ot.Segments))
		for _, s := range ot.Segments {
			actualSeg[s.Index] = s
		}
		for _, a := range st.Segments {
			v.Segments++
			if b, ok := actualSeg[a.Index]; ok && a.SHA256 == b.SHA256 {
				v.SegmentsMatched++
			}
		}
	}

	v.RoundtripRate = pct(v.FullSampleMatch, v.Samples)
	v.KeyframeHitRate = pct(v.KeyframesMatched, v.KeyframesExpected)
	v.SegmentHashRate = pct(v.SegmentsMatched, v.Segments)
	return v
}

func keyframeNumbers(t media.TrackInfo) map[uint32]bool {
	out := make(map[uint32]bool)
	if len(t.Keyframes) > 0 {
		for _, k := range t.Keyframes {
			out[k.SampleNumber] = true
		}
		return out
	}
	for _, s := range t.Samples {
		if s.Sync {
			out[s.Number] = true
		}
	}
	return out
}

// SortedKeyframeNumbers is a small helper for deterministic reports.
func SortedKeyframeNumbers(t media.TrackInfo) []uint32 {
	m := keyframeNumbers(t)
	out := make([]uint32, 0, len(m))
	for n := range m {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func pct(n, total int) float64 {
	if total == 0 {
		return 100
	}
	return 100 * float64(n) / float64(total)
}
