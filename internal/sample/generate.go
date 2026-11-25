// Package sample generates every fixture the kernel consumes from an integer
// seed: well-formed multi-representation manifests, RTP streams with
// deterministic jitter and loss, a truncated MP4 case, an illegal-timescale
// case and an HLS discontinuity playlist. No external files or network access
// are involved, and the same seed always yields identical bytes.
package sample

import (
	"fmt"

	"mediasync/internal/model"
	"mediasync/internal/pipeline"
	"mediasync/internal/rtp"
	"mediasync/internal/verify"
)

// PCG is a tiny deterministic pseudo-random generator (PCG hash step) so the
// fixtures never depend on the global math/rand source or its seed protocol.
type PCG struct {
	state uint64
}

// NewPCG seeds a generator from the command-line seed.
func NewPCG(seed int64) *PCG { return &PCG{state: uint64(seed)*6364136223846793005 + 1442695040888963407} }

// Intn returns a deterministic value in [0,n).
func (p *PCG) Intn(n int) int {
	if n <= 0 {
		return 0
	}
	p.state = p.state*6364136223846793005 + 1442695040888963407
	xorshift := ((p.state >> 18) ^ p.state) >> 27
	rot := p.state >> 59
	v := (xorshift >> rot) | (xorshift << ((-rot) & 31))
	return int(v % uint64(n))
}

// Params controls the simulated network conditions.
type Params struct {
	LossPct  int // 0..100 probability a packet is dropped
	JitterMS int // reorder window size in packets
}

// StreamResult is one representation's processed RTP evidence.
type StreamResult struct {
	Descriptors []model.SegmentDescriptor
	BufferStats rtp.Counters
	Recovery    rtp.RecoveryStats
	Losses      []*model.MediaError
}

// BuildLane simulates one representation end to end: ordered packets are
// permuted/dropped deterministically, pushed through a jitter buffer, then
// repaired by a windowed recoverer with redundancy for half the lost packets.
func BuildLane(rng *PCG, rep string, kind model.AssetKind, pri model.Priority,
	packets int, params Params) StreamResult {
	jb := rtp.NewJitterBuffer(rep, packets*2+4, params.JitterMS+4)

	type arrival struct {
		seq     uint16
		payload []byte
		dropped bool
	}
	originals := make([]arrival, packets)
	redundancy := map[uint16][]byte{}
	for i := 0; i < packets; i++ {
		drop := rng.Intn(100) < params.LossPct
		originals[i] = arrival{
			seq:     uint16(i),
			payload: []byte{byte(i), byte(i >> 8), 0x5A},
			dropped: drop,
		}
		if drop && i%2 == 0 {
			// Sender redundancy covers even-indexed losses; odd ones stay lost.
			redundancy[uint16(i)] = []byte{0xF0 ^ byte(i), 0x0F, 0xA5}
		}
	}

	// Deterministic jitter: swap each packet with a random neighbour inside
	// the jitter window before pushing to the buffer.
	order := make([]int, packets)
	for i := range order {
		order[i] = i
	}
	for i := range order {
		if params.JitterMS > 0 {
			j := i - rng.Intn(params.JitterMS+1)
			if j >= 0 {
				order[i], order[j] = order[j], order[i]
			}
		}
	}
	for _, idx := range order {
		a := originals[idx]
		if a.dropped {
			continue
		}
		_ = jb.Push(a.seq, a.payload)
	}

	ordered := jb.Flush()
	rec := rtp.NewRecoverer(rep, params.JitterMS+4, redundancy)
	final := rec.AnalyzeWindow(ordered, func(seq uint16) bool { return true })

	descs := make([]model.SegmentDescriptor, 0, len(final))
	for _, p := range final {
		descs = append(descs, model.SegmentDescriptor{
			Source: "dash", Representation: rep, PeriodID: 0, Index: int(p.Sequence),
			Kind: kind, Keyframe: p.Sequence%4 == 0,
			StartNS: int64(p.Sequence) * 1_000_000, DurationNS: 1_000_000,
			Timescale: 1000, StartInScale: int64(p.Sequence), DurationScale: 1,
			Payload: p.Payload,
		})
	}
	return StreamResult{
		Descriptors: descs, BufferStats: jb.Stats(),
		Recovery: rec.Stats(), Losses: rec.Losses(),
	}
}

// MP4Spec returns the round-trip spec for a healthy representation.
func MP4Spec(trackID uint32, kind model.AssetKind, segments int) ([]verify.TrackSpec, []verify.FragmentSpec) {
	tracks := []verify.TrackSpec{{
		TrackID: trackID, Timescale: 90000, Duration: uint64(segments) * 90000,
		Kind: kind,
	}}
	if kind == model.KindVideo {
		for i := 0; i < segments; i += 4 {
			tracks[0].KeyframeIndices = append(tracks[0].KeyframeIndices, uint32(i))
		}
	}
	frags := make([]verify.FragmentSpec, segments)
	for i := range frags {
		frags[i] = verify.FragmentSpec{
			TrackID: trackID, BaseDecodeTime: uint64(i) * 90000,
			SampleDuration: 90000, SampleCount: 1, Keyframe: i%4 == 0,
		}
	}
	return tracks, frags
}

// BuildAll constructs the full set of lane inputs for the acceptance run:
// three bitrate/asset lanes plus one deliberately corrupt MP4 lane.
func BuildAll(seed int64, params Params) []pipeline.LaneInput {
	rng := NewPCG(seed)
	specs := []struct {
		rep  string
		kind model.AssetKind
		pri  model.Priority
		n    int
		tid  uint32
	}{
		{"dialogue-core", model.KindDialogue, model.PriorityCore, 12, 2},
		{"subtitle-core", model.KindSubtitle, model.PriorityCore, 12, 3},
		{"video-hi", model.KindVideo, model.PriorityHigh, 12, 1},
		{"video-lo", model.KindVideo, model.PriorityLow, 12, 4},
	}
	lanes := make([]pipeline.LaneInput, 0, len(specs)+1)
	for _, s := range specs {
		stream := BuildLane(rng, s.rep, s.kind, s.pri, s.n, params)
		tracks, frags := MP4Spec(s.tid, s.kind, s.n)
		lanes = append(lanes, pipeline.LaneInput{
			Representation: s.rep, Kind: s.kind, Priority: s.pri,
			Segments: stream.Descriptors, BufferStats: stream.BufferStats,
			Recovery: stream.Recovery, Losses: stream.Losses,
			MP4Tracks: tracks, MP4Fragments: frags,
		})
	}
	// The corrupt lane models truncated MP4 input: its segments still parse at
	// the manifest level, but MP4 verification must locate and isolate it.
	corrupt := BuildLane(rng, "sfx-corrupt", model.KindSFX, model.PriorityLow, 8, params)
	lanes = append(lanes, pipeline.LaneInput{
		Representation: "sfx-corrupt", Kind: model.KindSFX, Priority: model.PriorityLow,
		Segments: corrupt.Descriptors, BufferStats: corrupt.BufferStats,
		Recovery: corrupt.Recovery, Losses: corrupt.Losses, CorruptMP4: true,
	})
	return lanes
}

// IllegalTimescaleManifest returns a DASH fixture containing a zero
// timescale, used by the command to prove the parser rejects it with a
// classified error.
func IllegalTimescaleManifest() []byte {
	return []byte(fmt.Sprintf("PERIOD 0\nREP broken video 1 0\nSEG 0 0 1000\n"))
}

// DiscontinuityPlaylist returns an HLS fixture with two discontinuity markers.
func DiscontinuityPlaylist() []byte {
	return []byte(`#EXTM3U
#EXT-X-TARGETDURATION:4
#EXTINF:2.0,video:video-hi:0:keyframe
a0.m4s
#EXT-X-DISCONTINUITY
#EXTINF:2.0,video:video-hi:0:keyframe
a1.m4s
#EXT-X-DISCONTINUITY
#EXTINF:2.0,subtitle:subs-core:0
a2.m4s
`)
}
