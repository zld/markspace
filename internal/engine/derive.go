package engine

import (
	"math"
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
		opts.DriftPPM,
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
	der.maxSkew = nearestSkew(audioTimes, videoTimes)

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
func absI64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func nearestSkew(audio, video []model.Nanos) model.Nanos {
	if len(audio) == 0 || len(video) == 0 {
		return 0
	}
	var max model.Nanos
	for _, a := range audio {
		lo := sort.Search(len(video), func(i int) bool { return video[i] >= a })
		best := model.Nanos(math.MaxInt64)
		if lo < len(video) {
			best = sync.Skew(a, video[lo])
		}
		if lo > 0 {
			if k := sync.Skew(a, video[lo-1]); k < best {
				best = k
			}
		}
		if best > max {
			max = best
		}
	}
	return max
}

// observations returns (arrival, media) samples for one track or kind. A
// trackID of zero means "any track of the given kind plus the explicit id".
func observations(log []accepted, trackID uint32, kind model.TrackKind, id uint32) []sync.Observation {
	out := make([]sync.Observation, 0, len(log)/2)
	for _, a := range log {
		if trackID != 0 {
			if a.TrackID != trackID {
				continue
			}
		} else if a.Kind != kind || (id != 0 && a.TrackID != id) {
			continue
		}
		out = append(out, sync.Observation{Arrival: a.Arrival, Media: a.Raw})
	}
	return out
}

// referenceTrackID is the lowest-id video track, used as the sync reference.
func referenceTrackID(tracks map[uint32]manifest.Track, order []uint32, log []accepted) uint32 {
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
