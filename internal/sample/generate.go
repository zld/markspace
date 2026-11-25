// Package sample generates every fixture the kernel consumes from an integer
// seed: well-formed multi-representation manifests, RTP streams with
// deterministic jitter and loss, a truncated MP4 case, an illegal-timescale
// case and an HLS discontinuity playlist. No external files or network access
// are involved, and the same seed always yields identical bytes.
package sample

import (
	"fmt"

	pionrtp "github.com/pion/rtp"

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
func NewPCG(seed int64) *PCG {
	return &PCG{state: uint64(seed)*6364136223846793005 + 1442695040888963407}
}

// Intn returns a deterministic value in [0,n). It applies the standard PCG
// 32-bit output transformation (xorshift plus a variable rotation) before the
// modulo, so values are uniformly distributed rather than clustered near zero.
func (p *PCG) Intn(n int) int {
	if n <= 0 {
		return 0
	}
	p.state = p.state*6364136223846793005 + 1442695040888963407
	xorshift := uint32(((p.state >> 18) ^ p.state) >> 27)
	rot := uint32(p.state >> 59)
	v := (xorshift >> rot) | (xorshift << ((-rot) & 31))
	return int(v % uint32(n))
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
	// The arrival reorder window is deliberately bounded independently of the
	// jitter-ms budget argument: a real network's reorder distance is a handful
	// of packets. jitter-ms scales the *probability* of a swap (capped at one
	// swap per packet), while the neighbour distance stays small and bounded.
	reorderDistance := 4
	swapPermille := params.JitterMS * 10 // 80ms -> 80% swap probability, capped below
	if swapPermille > 900 {
		swapPermille = 900
	}
	// The buffering/recovery window must comfortably exceed the maximum reorder
	// distance so a delayed packet is never misclassified as late; only genuine
	// sender-side losses reach the recovery logic.
	bufferWindow := reorderDistance*2 + 8
	jb := rtp.NewJitterBuffer(rep, packets*2+4, bufferWindow)

	type arrival struct {
		pkt     *pionrtp.Packet
		dropped bool
	}
	// Two deterministic losses per lane:
	//   - recoverableLoss (odd index) has sender redundancy and must be
	//     repaired inside the window;
	//   - forcedLoss (even index) has no redundancy and must remain a located,
	//     unrecovered loss rather than fabricated data.
	// Additional seed-driven random losses keep the fixture seed-sensitive;
	// odd-indexed random losses also carry redundancy.
	recoverableLoss := packets - 2 // odd when packets is even
	forcedLoss := (packets - 1) & ^1
	originals := make([]arrival, packets)
	redundancy := map[uint16][]byte{}
	for i := 0; i < packets; i++ {
		randomDrop := rng.Intn(100) < params.LossPct
		drop := i == forcedLoss || i == recoverableLoss || randomDrop
		originals[i] = arrival{
			pkt: &pionrtp.Packet{
				Header:  pionrtp.Header{SequenceNumber: uint16(i), Timestamp: uint32(i) * 90},
				Payload: []byte{byte(i), byte(i >> 8), 0x5A},
			},
			dropped: drop,
		}
		switch {
		case i == forcedLoss:
			// deliberately no redundancy: unrecoverable
		case drop && i%2 == 1:
			redundancy[uint16(i)] = []byte{0xF0 ^ byte(i), 0x0F, 0xA5}
		}
	}

	// Deterministic jitter: each packet has a jitter-ms-scaled probability of
	// being swapped with a nearby neighbour inside the bounded reorder distance.
	order := make([]int, packets)
	for i := range order {
		order[i] = i
	}
	for i := range order {
		if i > 0 && rng.Intn(1000) < swapPermille {
			j := i - 1 - rng.Intn(reorderDistance)
			if j < 0 {
				j = 0
			}
			order[i], order[j] = order[j], order[i]
		}
	}
	for _, idx := range order {
		a := originals[idx]
		if a.dropped {
			continue // lost packets never reach the receiver
		}
		_ = jb.Push(a.pkt)
	}

	// The recoverer inspects the buffered survivors. Because the buffer never
	// saw the dropped sequences, they surface as interior gaps and are either
	// repaired from redundancy or retained as located losses.
	ordered := jb.Flush()
	rec := rtp.NewRecoverer(rep, bufferWindow, redundancy)
	final := rec.AnalyzeWindow(ordered, func(seq uint16) bool { return true })

	// Descriptors are emitted in playout order on a compact 1ms grid: a lost
	// sequence leaves a located recovery/loss record rather than a timeline
	// hole, so adjacent output segments remain contiguous on the shared clock.
	// Every emitted fragment starts on a legal sync-sample cut; the finer
	// 4-sample GOP structure is expressed separately in the MP4 stss indices.
	descs := make([]model.SegmentDescriptor, 0, len(final))
	for pos, p := range final {
		descs = append(descs, model.SegmentDescriptor{
			Source: "dash", Representation: rep, PeriodID: 0,
			Index: int(p.SequenceNumber),
			Kind:  kind, Keyframe: true,
			StartNS: int64(pos) * 1_000_000, DurationNS: 1_000_000,
			Timescale: 1000, StartInScale: int64(pos), DurationScale: 1,
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
	// The corrupt lane models truncated MP4 input: a real fragmented MP4 is
	// generated then physically cut, and its segments still parse at the
	// manifest level. Verification must locate the truncation and isolate it.
	corrupt := BuildLane(rng, "sfx-corrupt", model.KindSFX, model.PriorityLow, 8, params)
	corruptTracks, corruptFrags := MP4Spec(5, model.KindSFX, 8)
	truncated := truncatedMP4(corruptTracks, corruptFrags)
	lanes = append(lanes, pipeline.LaneInput{
		Representation: "sfx-corrupt", Kind: model.KindSFX, Priority: model.PriorityLow,
		Segments: corrupt.Descriptors, BufferStats: corrupt.BufferStats,
		Recovery: corrupt.Recovery, Losses: corrupt.Losses,
		MP4Tracks: corruptTracks, MP4Fragments: corruptFrags,
		CorruptMP4: true, CorruptMP4Data: truncated,
	})
	return lanes
}

// truncatedMP4 writes a valid fragmented MP4 and cuts it halfway through,
// producing a genuinely unparseable, locatable truncation fixture.
func truncatedMP4(tracks []verify.TrackSpec, frags []verify.FragmentSpec) []byte {
	data, err := verify.WriteFragmentedMP4(tracks, frags)
	if err != nil {
		// The inputs are internally generated and always valid to write.
		panic(fmt.Sprintf("fixture MP4 write failed: %v", err))
	}
	return data[:len(data)/2]
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
