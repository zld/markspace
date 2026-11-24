package engine

import (
	"sort"

	"mediasync/internal/manifest"
	"mediasync/internal/model"
	"mediasync/internal/output"
	"mediasync/internal/recovery"
	"mediasync/internal/sync"
)

// delivery is the derived output plus the counters computed alongside it.
type delivery struct {
	records       []output.Record
	lost          map[uint32]int64
	recovered     map[uint32]int64
	unrecoverable map[uint32]int64
	maxSkew       model.Nanos
	driftPPM      float64
	driftClamped  bool
}

// deriveRecords converts the accepted event log into delivered records.
//
// Ordering uses each sample's own timestamp: the accepted log is sorted by
// uncorrected media time per track to establish the VFR order, then drift is
// estimated from adjacent audio/video arrivals, applied only to audio, and the
// final delivery is sorted by corrected time. No sample is ever inserted on a
// fixed grid, and equal timestamps keep a deterministic track/sequence order.
func deriveRecords(acc []accepted, repairs []adminRepair, tracks map[uint32]manifest.Track, order []uint32, opts Options) delivery {
	der := delivery{
		lost: map[uint32]int64{}, recovered: map[uint32]int64{}, unrecoverable: map[uint32]int64{},
	}
	log := sortedAccepted(acc)

	// Estimate the audio clock's rate relative to the reference camera. Each
	// track is regressed against capture arrival; jitter attenuates both
	// slopes equally, so their ratio is the relative drift.
	refTrack := referenceTrackID(tracks, order, log)
	drift := sync.EstimateClockDrift(
		observations(log, refTrack, model.KindVideo, 0),
		observations(log, 0, model.KindAudio, firstAudioTrack(tracks, order)),
		sync.Config{MaxPPM: opts.DriftPPM, Window: 256, MinSamples: 8},
	)
	der.driftPPM = drift.PPM
	der.driftClamped = drift.Clamped
	ratio := drift.Ratio
	if drift.Insufficient {
		ratio = 1
	}

	// Anchor each audio track at its first accepted sample.
	anchor := map[uint32]model.Nanos{}
	for _, a := range log {
		if _, ok := anchor[a.TrackID]; !ok {
			anchor[a.TrackID] = a.Raw
		}
	}
	corrected := make([]model.Nanos, len(log))
	for i, a := range log {
		c := a.Raw
		if a.Kind == model.KindAudio {
			c = sync.ApplyClockDrift(a.Raw, anchor[a.TrackID], ratio)
		}
		corrected[i] = c
	}

	idx := make([]int, len(log))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(i, j int) bool {
		a, b := log[idx[i]], log[idx[j]]
		ca, cb := corrected[idx[i]], corrected[idx[j]]
		if ca != cb {
			return ca < cb
		}
		if a.TrackID != b.TrackID {
			return a.TrackID < b.TrackID
		}
		return a.Ext < b.Ext
	})

	der.records = make([]output.Record, 0, len(idx))
	// Audio/video skew is measured nearest-neighbour: for every audio sample,
	// the closest video sample in corrected time. Comparing running "latest"
	// values would report half a frame of the sampling grid as sync error.
	var audioTimes, videoTimes []model.Nanos
	for _, i := range idx {
		a := log[i]
		c := corrected[i]
		der.records = append(der.records, output.Record{
			TrackID: a.TrackID, Kind: a.Kind, ExtSeq: a.Ext,
			Keyframe: a.Keyframe, Original: a.Raw, Corrected: c, Size: a.Size,
		})
		if a.Kind == model.KindAudio {
			audioTimes = append(audioTimes, c)
		} else {
			videoTimes = append(videoTimes, c)
		}
	}
	// A/V skew compares the media timestamps of audio and video packets that
	// arrived together on the same capture clock. Buckets that contain only
	// one media type (a lost span, or a sampling gap) contribute nothing; that
	// span is reported by loss classification instead of masquerading as sync
	// error.
	der.maxSkew = pairedSkew(log, corrected, refTrack, firstAudioTrack(tracks, order))

	// Per-track loss classification over the delivered sequence numbers.
	byTrack := map[uint32][]recovery.Packet{}
	for _, i := range idx {
		a := log[i]
		byTrack[a.TrackID] = append(byTrack[a.TrackID], recovery.Packet{
			Ext: a.Ext, TrackID: a.TrackID, Kind: a.Kind, Keyframe: a.Keyframe,
			MediaTime: corrected[i],
		})
	}
	repByTrack := map[uint32][]recovery.Repair{}
	for _, r := range repairs {
		repByTrack[r.TrackID] = append(repByTrack[r.TrackID], recovery.Repair{Source: r.Source, Targets: r.Targets})
	}
	// Derive per-packet cadence from the manifest keyframe spacing for video
	// and from the median audio interval for audio.
	for _, id := range order {
		t := tracks[id]
		pkts := byTrack[id]
		in := recovery.Input{TrackID: id, Kind: t.Kind, Packets: pkts, Repairs: repByTrack[id]}
		if t.Kind == model.KindAudio && len(pkts) > 1 {
			in.AudioCadence = medianDelta(pkts)
		}
		if kfs, err := t.KeyframeTimes(); err == nil {
			in.KeyframeTimes = kfs
		}
		rep := recovery.Classify(in)
		var lost, rec, unrec int64
		for _, g := range rep.Gaps {
			lost += g.Count
			rec += g.Redundant
			if !g.Recoverable {
				unrec += g.Count - g.Redundant
			}
		}
		der.lost[id] = lost
		der.recovered[id] = rec
		der.unrecoverable[id] = unrec
	}
	return der
}

// nearestSkew returns the largest distance from an audio sample to its
// nearest video sample. Both inputs are ascending.
// pairedSkew measures residual audio/video divergence after correction.
//
// Both streams are bucketed by corrected media time. Within each half-second
// bucket that has full coverage, the median audio time and median video time
// describe the same wall-clock interval, so their difference is the A/V phase
// there; per-sample jitter and sampling-grid phase average out. A constant
// phase is a property of the declared starts, so only divergence from the
// median phase is reported — growth across the capture is exactly what drift
// correction must remove.
//
// Buckets with degraded video coverage are skipped. A lost burst leaves a
// genuine hole in one stream, which loss classification reports; letting it
// shift a bucket median would turn a capture gap into a phantom sync fault.
func pairedSkew(log []accepted, corrected []model.Nanos, refTrack, audioTrack uint32) model.Nanos {
	const bucket = model.Nanos(model.NanosPerMilli * 500)
	type cell struct {
		audio, video []model.Nanos
	}
	buckets := map[int64]*cell{}
	for i, e := range log {
		if e.TrackID != refTrack && e.TrackID != audioTrack {
			continue
		}
		k := int64(corrected[i]) / int64(bucket)
		c := buckets[k]
		if c == nil {
			c = &cell{}
			buckets[k] = c
		}
		if e.TrackID == refTrack {
			c.video = append(c.video, corrected[i])
		} else {
			c.audio = append(c.audio, corrected[i])
		}
	}
	// Full coverage is the modal sample count. A partially lost bucket has
	// fewer samples, so it is excluded; using the maximum would be brittle if
	// one bucket happened to contain a boundary packet.
	hist := map[int]int{}
	mode := 0
	modeCount := 0
	for _, c := range buckets {
		for _, n := range []int{len(c.video), len(c.audio)} {
			hist[n]++
			if hist[n] > modeCount || (hist[n] == modeCount && n > mode) {
				mode, modeCount = n, hist[n]
			}
		}
	}
	if mode < 3 {
		return 0
	}
	keys := make([]int64, 0, len(buckets))
	for k, c := range buckets {
		if len(c.audio) >= mode && len(c.video) >= mode {
			keys = append(keys, k)
		}
	}
	if len(keys) < 2 {
		return 0
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	diffs := make([]model.Nanos, 0, len(keys))
	for _, k := range keys {
		c := buckets[k]
		diffs = append(diffs, medianNanos(c.audio)-medianNanos(c.video))
	}
	base := medianNanos(diffs)
	var max model.Nanos
	for _, d := range diffs {
		if k := sync.Skew(d, base); k > max {
			max = k
		}
	}
	return max
}

// medianDelta is the median gap between consecutive packet media times.
func medianDelta(pkts []recovery.Packet) model.Nanos {
	if len(pkts) < 2 {
		return 0
	}
	var deltas []model.Nanos
	for i := 1; i < len(pkts); i++ {
		d := pkts[i].MediaTime - pkts[i-1].MediaTime
		if d > 0 {
			deltas = append(deltas, d)
		}
	}
	if len(deltas) == 0 {
		return 0
	}
	sort.SliceStable(deltas, func(i, j int) bool { return deltas[i] < deltas[j] })
	return deltas[len(deltas)/2]
}

// observations returns (arrival, media) samples for one track.
func observations(log []accepted, trackID uint32, _ model.TrackKind, _ uint32) []sync.Observation {
	out := make([]sync.Observation, 0, len(log)/2)
	for _, a := range log {
		if a.TrackID != trackID {
			continue
		}
		out = append(out, sync.Observation{Arrival: a.Arrival, Media: a.Raw})
	}
	return out
}

// referenceTrackID is the lowest-id video track, used as the sync reference.
func referenceTrackID(tracks map[uint32]manifest.Track, order []uint32, _ []accepted) uint32 {
	for _, id := range order {
		if tracks[id].Kind == model.KindVideo {
			return id
		}
	}
	return 0
}

// firstAudioTrack is the lowest-id audio track.
func firstAudioTrack(tracks map[uint32]manifest.Track, order []uint32) uint32 {
	for _, id := range order {
		if tracks[id].Kind == model.KindAudio {
			return id
		}
	}
	return 0
}

func absI64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func medianNanos(v []model.Nanos) model.Nanos {
	if len(v) == 0 {
		return 0
	}
	s := append([]model.Nanos(nil), v...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[len(s)/2]
}
