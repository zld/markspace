package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"time"

	"mediadist/internal/demux"
	"mediadist/internal/transport"
)

// buildSegments aggregates unified-timeline segments across all streams. Each
// stream contributes its own sliding-window bandwidth estimate; the estimate
// only sets buffer capacity and never touches media timestamps.
func buildSegments(dm *demux.Result, opts Options) []SegmentReport {
	segmentNS := int64(opts.SegmentMS) * 1e6
	// Segment zero starts at the earliest stream origin, so reported wall
	// times line up with each stream's declared first timestamp.
	originNS := int64(0)
	for i, st := range dm.Streams {
		if i == 0 || st.Spec.OriginNS < originNS {
			originNS = st.Spec.OriginNS
		}
	}
	type acc struct {
		samples   int
		bytes     int64
		lost      int64
		keyframes int
		digests   []string
		observed  int64
		estimates int
	}
	bySeg := map[int]*acc{}
	nominalBps := int64(opts.BandwidthKbps) * 1000

	for _, st := range dm.Streams {
		est, err := transport.NewEstimator(st.Clock, transport.DefaultEstimatorConfig())
		if err != nil {
			continue
		}
		var inputs []transport.FragmentSample
		for _, s := range st.Samples {
			a, ok := bySeg[s.Segment]
			if !ok {
				a = &acc{}
				bySeg[s.Segment] = a
			}
			a.samples++
			a.bytes += int64(len(s.Data))
			a.lost += int64(s.MissingPackets)
			if s.Keyframe {
				a.keyframes++
			}
			a.digests = append(a.digests, fmt.Sprintf("%d:%s", s.Stream, canonicalDigest(s)))
			inputs = append(inputs, transport.FragmentSample{
				Segment: s.Segment, Bytes: int64(len(s.Data)),
				Missing: int64(s.MissingPackets), Tick: s.PTS, Keyframe: s.Keyframe,
			})
		}
		for _, fr := range transport.AggregateFragments(inputs, est, 2*time.Second) {
			a, ok := bySeg[fr.Index]
			if !ok {
				continue
			}
			if fr.RateBps > 0 {
				a.observed += fr.RateBps
				a.estimates++
			}
		}
	}

	idx := make([]int, 0, len(bySeg))
	for k := range bySeg {
		idx = append(idx, k)
	}
	sort.Ints(idx)
	out := make([]SegmentReport, 0, len(idx))
	for _, k := range idx {
		a := bySeg[k]
		sort.Strings(a.digests)
		h := sha256.New()
		for _, d := range a.digests {
			io.WriteString(h, d)
		}
		seg := SegmentReport{
			Index:   k,
			StartNS: originNS + int64(k)*segmentNS,
			EndNS:   originNS + int64(k+1)*segmentNS,
			Samples: a.samples, Bytes: a.bytes, Lost: a.lost, Keyframes: a.keyframes,
			SHA256: hex.EncodeToString(h.Sum(nil)),
		}
		if a.estimates > 0 {
			seg.RateBps = a.observed / int64(a.estimates)
		}
		// The declared bandwidth caps the estimate so a bursty segment cannot
		// inflate the buffering budget past what the operator provisioned.
		if nominalBps > 0 && seg.RateBps > nominalBps {
			seg.RateBps = nominalBps
		}
		// Capacity is two seconds at the estimated rate. It is derived only
		// from received bytes over the segment's own tick span.
		seg.BufferCap = seg.RateBps / 8 * 2
		out = append(out, seg)
	}
	return out
}
