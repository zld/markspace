package fixture

import (
	"mediacheck/internal/clock"
	"mediacheck/internal/model"
)

// videoLevel is one rung of the multi-bitrate ladder.
type videoLevel struct {
	id      string
	bitrate int64
}

// Generate synthesizes a complete program from seed. The same seed and fault
// set always yields byte-identical structures.
func Generate(seed int64, faults []model.FaultKind) *Program {
	has := func(k model.FaultKind) bool {
		for _, f := range faults {
			if f == k {
				return true
			}
		}
		return false
	}

	levels := []videoLevel{
		{"v-high", 2_400_000},
		{"v-mid", 1_200_000},
		{"v-low", 600_000},
	}
	prog := &Program{Seed: seed, Faults: append([]model.FaultKind(nil), faults...)}
	baseColor := TrackMeta{ColorPrimaries: 1, TransferChar: 1, MatrixCoeffs: 1}

	for idx, lv := range levels {
		r := newVideoRep(seed, lv.id, lv.bitrate, baseColor)
		injectVideoFaults(r, idx, has)
		prog.Video = append(prog.Video, r)
	}
	audioSpecs := []struct {
		id      string
		bitrate int64
	}{{"a-stereo", 128_000}, {"a-surround", 96_000}}
	for idx, sp := range audioSpecs {
		r := newAudioRep(seed, sp.id, sp.bitrate, TrackMeta{Channels: 2, SampleRate: 48000})
		injectAudioFaults(r, idx, has)
		prog.Audio = append(prog.Audio, r)
	}
	fillManifestTicks(prog)
	return prog
}

func newVideoRep(seed int64, id string, bitrate int64, color TrackMeta) *Representation {
	const (
		timescale = int64(25000)
		segDur    = int64(10000) // 0.4 s
		fps       = int64(25)
	)
	rng := rngFrom(seed, "video:"+id)
	r := &Representation{
		ID: id, Kind: "video", Timescale: timescale, TargetBitrate: bitrate,
		SegmentDuration: segDur, Meta: color,
	}
	frameDur := timescale / fps
	for s := 0; s < nominalSegmentCount; s++ {
		seg := &Segment{Sequence: uint32(s), Timescale: timescale,
			BaseDecode: int64(s) * segDur, Duration: segDur, IndexPresent: true}
		payload := 0
		for f := 0; f < int(segDur/frameDur); f++ {
			isKey := f == 0
			// Deterministic size: key frames are larger, P frames jitter ±3%.
			frameBytes := frameSize(bitrate, fps, isKey, true, rng)
			payload += frameBytes
			seg.Samples = append(seg.Samples, Sample{
				IsKeyframe: isKey, Duration: frameDur, Bytes: frameBytes,
				Quality: boundedQuality(rng),
			})
		}
		seg.Data = sizedPayload(id, s, payload)
		r.Segments = append(r.Segments, seg)
	}
	return r
}

func newAudioRep(seed int64, id string, bitrate int64, meta TrackMeta) *Representation {
	const (
		timescale = int64(48000)
		segDur    = int64(19200) // 0.4 s
		frameDur  = int64(192)   // 4 ms frames
	)
	rng := rngFrom(seed, "audio:"+id)
	r := &Representation{
		ID: id, Kind: "audio", Timescale: timescale, TargetBitrate: bitrate,
		SegmentDuration: segDur, Meta: meta,
	}
	for s := 0; s < nominalSegmentCount; s++ {
		seg := &Segment{Sequence: uint32(s), Timescale: timescale,
			BaseDecode: int64(s) * segDur, Duration: segDur, IndexPresent: false}
		payload := 0
		for f := 0; f < int(segDur/frameDur); f++ {
			b := frameSize(bitrate, timescale/frameDur, false, false, rng)
			payload += b
			seg.Samples = append(seg.Samples, Sample{
				Duration: frameDur, Bytes: b, Quality: boundedQuality(rng),
			})
		}
		seg.Data = sizedPayload(id, s, payload)
		r.Segments = append(r.Segments, seg)
	}
	return r
}

// injectVideoFaults places faults at fixed positions so runs are reproducible.
func injectVideoFaults(r *Representation, levelIdx int, has func(model.FaultKind) bool) {
	if !has(model.FaultTruncate) && !has(model.FaultLate) &&
		!has(model.FaultDuplicate) && !has(model.FaultMetaConflict) {
		return
	}
	if has(model.FaultTruncate) && levelIdx == 1 {
		seg := r.Segments[3]
		seg.Truncated = true
		cut := len(seg.Data) * 3 / 5
		seg.Data = seg.Data[:cut]
		// Drop the trailing samples; actual duration follows the survivors.
		kept := len(seg.Samples) * 3 / 5
		seg.Samples = seg.Samples[:kept]
		seg.Duration = samplesDuration(seg.Samples)
	}
	if has(model.FaultTruncate) && levelIdx == 0 {
		// A second damaged segment on another representation guarantees the
		// acceptance threshold of at least two locatable damaged ranges.
		seg := r.Segments[6]
		seg.Truncated = true
		cut := len(seg.Data) * 2 / 3
		seg.Data = seg.Data[:cut]
		kept := len(seg.Samples) * 2 / 3
		seg.Samples = seg.Samples[:kept]
		seg.Duration = samplesDuration(seg.Samples)
	}
	if has(model.FaultLate) && levelIdx == 0 {
		r.Segments[5].Late = true
	}
	if has(model.FaultDuplicate) && levelIdx == 2 {
		r.Segments[2].Duplicate = true
	}
	if has(model.FaultMetaConflict) && levelIdx == 2 {
		// BT.2020 primaries on segments 4..6, conflicting with BT.709 peers.
		r.Meta = TrackMeta{ColorPrimaries: 9, TransferChar: 1, MatrixCoeffs: 9}
	}
}

func injectAudioFaults(r *Representation, levelIdx int, has func(model.FaultKind) bool) {
	if !has(model.FaultMetaConflict) {
		return
	}
	if levelIdx == 1 {
		r.Meta.Channels = 6       // 5.1 layout vs stereo peer
		r.Meta.SampleRate = 44100 // rate mismatch vs 48 kHz peer
	}
}

// fillManifestTicks projects every planned segment start onto master ticks.
func fillManifestTicks(p *Program) {
	all := append(append([]*Representation{}, p.Video...), p.Audio...)
	for _, r := range all {
		r.ManifestStarts = make([]int64, len(r.Segments))
		for i, seg := range r.Segments {
			tick, err := clock.ToMaster(seg.BaseDecode, r.Timescale)
			if err != nil {
				panic("fixture: non-lossless base decode time: " + err.Error())
			}
			r.ManifestStarts[i] = tick
		}
	}
}
