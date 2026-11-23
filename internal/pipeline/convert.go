package pipeline

import (
	"sort"

	"archivectl/internal/ebml"
	"archivectl/internal/fixture"
	"archivectl/internal/render"
	"archivectl/internal/timeline"
)

// newSegmentSet builds one deterministic segment from a base seed and index.
func newSegmentSet(baseSeed, index int64, lossWindow int) *fixture.Set {
	return fixture.Generate(baseSeed*1_000_003 + index, lossWindow)
}

// smallRenderOptions keeps per-segment rendering cheap in the benchmark.
func smallRenderOptions() render.FrameOptions {
	return render.FrameOptions{Width: 16, Height: 9, FrameStepNs: 80_000_000}
}

// buildGraphAndSamples derives a two-track graph (video=1, audio=2) and
// converts every fully recovered block from all documents into samples.
func buildGraphAndSamples(docs map[string]*ebml.Document) (*timeline.Graph, []timeline.Sample) {
	g := timeline.NewGraph()
	_ = g.Add(timeline.Track{Number: 1, Kind: timeline.KindVideo,
		DefaultDurationNs: 40_000_000})
	_ = g.Add(timeline.Track{Number: 2, Kind: timeline.KindAudio,
		DependsOn: []uint32{1}})

	seen := map[[2]int64]bool{}
	var samples []timeline.Sample
	for _, doc := range docs {
		scale := doc.TimecodeScaleNs
		for _, c := range doc.Clusters {
			for _, b := range c.Blocks {
				track := b.TrackNumber
				if !g.Has(track) {
					continue // unknown track reference is rejected
				}
				key := [2]int64{b.TimecodeNs, b.Offset}
				if seen[key] {
					continue
				}
				seen[key] = true
				samples = append(samples, timeline.Sample{
					TrackNumber: track,
					StartNs:     b.TimecodeNs,
					DurationNs:  blockDuration(track, scale),
					Keyframe:    b.Keyframe,
					Offset:      b.Offset,
					Size:        len(b.Data),
				})
			}
		}
	}
	return g, samples
}

func blockDuration(track uint32, scaleNs int64) int64 {
	if track == 1 {
		return 40_000_000
	}
	return 26_000_000 // sparse, coarser audio cadence
}

func renderPoints(m *timeline.Merged) []render.SamplePoint {
	out := make([]render.SamplePoint, 0, len(m.Samples))
	for _, s := range m.Samples {
		out = append(out, render.SamplePoint{
			TrackNumber: s.TrackNumber,
			StartNs:     s.StartNs,
			EndNs:       s.EndNs(),
			Keyframe:    s.Keyframe,
		})
	}
	return out
}

func audioPoints(m *timeline.Merged) []render.AudioPoint {
	var out []render.AudioPoint
	for _, s := range m.Samples {
		if s.TrackNumber != 2 {
			continue
		}
		out = append(out, render.AudioPoint{
			TrackNumber: s.TrackNumber,
			StartNs:     s.StartNs,
			EndNs:       s.EndNs(),
			Payload:     make([]byte, s.Size),
		})
	}
	return out
}

// AV sync: for each video sample, find the nearest audio start time and
// record the absolute gap in milliseconds. The P95 must stay within 20 ms on
// the healthy fixture.
func avSyncGapsMs(m *timeline.Merged) []float64 {
	var video, audio []int64
	for _, s := range m.Samples {
		if s.TrackNumber == 1 {
			video = append(video, s.StartNs)
		} else {
			audio = append(audio, s.StartNs)
		}
	}
	sort.Slice(audio, func(i, j int) bool { return audio[i] < audio[j] })
	var gaps []float64
	for _, vt := range video {
		if len(audio) == 0 {
			continue
		}
		best := nearestDistance(vt, audio)
		gaps = append(gaps, float64(best)/1_000_000.0)
	}
	return gaps
}

func nearestDistance(t int64, sorted []int64) int64 {
	i := sort.Search(len(sorted), func(i int) bool { return sorted[i] >= t })
	best := int64(0)
	set := false
	consider := func(v int64) {
		d := v - t
		if d < 0 {
			d = -d
		}
		if !set || d < best {
			best, set = d, true
		}
	}
	if i < len(sorted) {
		consider(sorted[i])
	}
	if i > 0 {
		consider(sorted[i-1])
	}
	return best
}

// percentile returns the p-th percentile (0..100) using nearest-rank.
func percentile(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	rank := int((p/100.0)*float64(len(sorted)) + 0.999999)
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}
