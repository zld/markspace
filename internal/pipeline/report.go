package pipeline

import (
	"sort"

	"example.com/mediaforge/internal/adapter"
	"example.com/mediaforge/internal/clock"
	"example.com/mediaforge/internal/index"
	"example.com/mediaforge/internal/merrors"
	"example.com/mediaforge/internal/segment"
	"example.com/mediaforge/internal/syncer"
)

// countTruncated parses the deliberately damaged fixture and returns how many
// truncated-sample reports the adapter produced. A file that fails outright
// counts as one defect so the demo's invariant still sees it.
func countTruncated(buf []byte, plug adapter.Adapter) int {
	if len(buf) == 0 {
		return 0
	}
	res, err := plug.Parse(buf)
	if err != nil {
		return 1
	}
	n := 0
	for _, w := range res.Warnings {
		if merrors.IsCategory(w, merrors.Truncated) {
			n++
		}
	}
	return n
}

// estimateDrift folds Sender Reports through the drift estimator. Reports are
// ordered by NTP time so file order cannot influence the estimate. A rejected
// report contributes nothing to the committed value.
func estimateDrift(reports []SRInput, clockRate uint32) int64 {
	if len(reports) == 0 {
		return 0
	}
	est, err := clock.NewDriftEstimator(clockRate)
	if err != nil {
		return 0
	}
	sorted := append([]SRInput(nil), reports...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Report.NTPMicros() < sorted[j].Report.NTPMicros()
	})
	var last int64
	for _, r := range sorted {
		ppm, err := est.Observe(r.Report, r.RTPExt)
		if err != nil {
			continue
		}
		last = ppm
	}
	return last
}

// videoRate returns the clock rate of the video track, used as the reference
// rate for drift estimation when no explicit rate is configured.
func videoRate(in Input) uint32 {
	if in.ReferenceRate != 0 {
		return in.ReferenceRate
	}
	for _, t := range in.Tracks {
		if t.Kind == syncer.KindVideo {
			return t.ClockRate
		}
	}
	return 90000
}

func findTrackInfo(tracks []adapter.TrackInfo, id uint32) int {
	for i := range tracks {
		if tracks[i].ID == id {
			return i
		}
	}
	return -1
}

// expectedSamples is the number of samples the container declares across the
// streams the scenario actually feeds.
func expectedSamples(c *adapter.Container, inputs []TrackInput) int {
	n := 0
	for _, tr := range inputs {
		if i := findTrackInfo(c.Tracks, tr.MP4TrackID); i >= 0 {
			n += c.Tracks[i].SampleCnt
		}
	}
	return n
}

// videoTrack returns the container track fed by the video stream.
func videoTrack(c *adapter.Container, inputs []TrackInput) (*adapter.TrackInfo, []adapter.Sample) {
	for _, tr := range inputs {
		if tr.Kind != syncer.KindVideo {
			continue
		}
		if i := findTrackInfo(c.Tracks, tr.MP4TrackID); i >= 0 {
			return &c.Tracks[i], c.Samples[tr.MP4TrackID]
		}
	}
	// Fall back to the first track the container declares.
	if len(c.Tracks) == 0 {
		return nil, nil
	}
	return &c.Tracks[0], c.Samples[c.Tracks[0].ID]
}

func indexBuild(timescale uint32, durs []uint64, keys []bool) (*index.Index, error) {
	return index.Build(timescale, durs, keys)
}

// keyframeHitRate measures how often a lookup at an arbitrary sample start
// returns that sample or an earlier keyframe - the property a cutter needs.
// Sampling every sample would make the metric trivially 1 for a dense index,
// so targets are spread across the timeline at a fixed stride and each must
// resolve to a keyframe at or before the target.
func keyframeHitRate(idx *index.Index, samples []adapter.Sample) float64 {
	if idx.Empty() || idx.EndTick() == 0 {
		return 0
	}
	const probes = 200
	hits := 0
	total := 0
	step := idx.EndTick() / probes
	if step == 0 {
		step = 1
	}
	for t := uint64(0); t < idx.EndTick(); t += step {
		total++
		e, err := idx.Lookup(t)
		if err != nil {
			// A clamp before the first keyframe still yields a decodable
			// start, which is a hit; other errors are misses.
			if !merrors.IsCategory(err, merrors.OutOfRange) {
				continue
			}
		}
		if e.Tick <= t {
			hits++
		}
	}
	if total == 0 {
		return 0
	}
	return float64(hits) / float64(total)
}

// deliveredSample is the pipeline's view of one aligned, delivered unit.
type deliveredSample struct {
	track     syncer.Kind
	trackID   uint32
	sample    adapter.Sample
	aligned   syncer.Aligned
	recovered bool
}

// buildSegments assigns every delivered sample to a segment cut and hashes
// each segment. Assignment is by common clock tick, so a sample belongs to the
// segment whose cut is the latest at or before it - the same rule a cutter
// would use.
func buildSegments(cuts []index.Entry, idx *index.Index,
	delivered []deliveredSample, rep *Report) []segment.Hash {
	segs := make([]segment.Segment, len(cuts))
	for i, c := range cuts {
		start := c.Tick
		end := idx.EndTick()
		if i+1 < len(cuts) {
			end = cuts[i+1].Tick
		}
		segs[i] = segment.Segment{Index: i, StartTick: start, EndTick: end}
	}
	for _, d := range delivered {
		si := segmentFor(cuts, d.aligned.CommonTick)
		if si < 0 {
			continue
		}
		segs[si].Samples = append(segs[si].Samples, segment.Sample{
			TrackSSRC:  ssrcof(d),
			CommonTick: d.aligned.CommonTick,
			Duration:   d.sample.Duration,
			Keyframe:   d.sample.IsKeyframe,
			Recovered:  d.recovered,
			Payload:    d.sample.Data,
		})
	}
	hashes, err := segment.ComputeAll(segs)
	if err != nil {
		rep.Record(err)
		return nil
	}
	return hashes
}

func ssrcof(d deliveredSample) uint32 {
	return d.aligned.SSRC
}

// segmentFor returns the index of the last cut at or before tick, or -1.
func segmentFor(cuts []index.Entry, tick uint64) int {
	lo, hi := 0, len(cuts)
	for lo < hi {
		mid := (lo + hi) / 2
		if cuts[mid].Tick <= tick {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo == 0 {
		return -1
	}
	return lo - 1
}

// verifySegments re-hashes nothing but checks the list's structural invariants:
// contiguous indices and increasing bounds.
func verifySegments(hashes []segment.Hash) error {
	var prev segment.Hash
	for i, h := range hashes {
		if h.Index != i {
			return merrors.New(merrors.StageSegment, merrors.Invalid,
				"hash %d has index %d", i, h.Index)
		}
		if i > 0 && h.StartTick < prev.StartTick {
			return merrors.New(merrors.StageSegment, merrors.Invalid,
				"segment %d starts before its predecessor", h.Index)
		}
		prev = h
	}
	return nil
}

// remuxContainer rebuilds a container from the delivered samples using the
// adapter, proving the parse/encode path on real data. Sample timing and sync
// flags come from the source metadata, not from the network.
func remuxContainer(src *adapter.Container, delivered []deliveredSample, in Input) ([]byte, error) {
	out := &adapter.Container{
		Format:  adapter.FormatMP4,
		Major:   adapter.MP4V1,
		Samples: make(map[uint32][]adapter.Sample),
	}
	for _, info := range src.Tracks {
		out.Tracks = append(out.Tracks, info)
	}
	for _, d := range delivered {
		if len(d.sample.Data) == 0 {
			continue
		}
		out.Samples[d.trackID] = append(out.Samples[d.trackID], d.sample)
	}
	for _, info := range src.Tracks {
		ss := out.Samples[info.ID]
		sort.SliceStable(ss, func(i, j int) bool { return ss[i].Index < ss[j].Index })
		out.Samples[info.ID] = ss
		out.Tracks[findTrackInfo(out.Tracks, info.ID)].SampleCnt = len(ss)
	}
	reg := adapter.NewRegistry()
	if err := reg.Register(adapter.NewMP4Adapter()); err != nil {
		return nil, err
	}
	plug, err := reg.Resolve(adapter.FormatMP4, adapter.MP4V1)
	if err != nil {
		return nil, err
	}
	return plug.Encode(out)
}
