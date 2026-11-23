// Package fixture generates fully synthetic, deterministic RTP streams,
// bare codec frames and keyframe indexes. No external files or network
// sources are used; every byte derives from a seed so repeated runs are
// byte-identical.
package fixture

import (
	"math/rand"

	"mediaroute/internal/codec"
	"mediaroute/internal/transport"
)

// TrackSpec describes one synthetic media track.
type TrackSpec struct {
	SSRC     uint32
	Codec    codec.Codec
	Frames   int    // total frames generated
	KeyEvery int    // a keyframe every Nth frame (0-based cadence)
	SeqStart uint16 // non-zero / wrap-friendly RTP sequence origin
	TSStart  uint32 // non-zero RTP timestamp origin
	TSIncr   uint32 // timestamp ticks per frame
	TSWrap   bool   // force a 32-bit timestamp wraparound mid-stream
}

// Config controls the injected transmission impairments.
type Config struct {
	Seed          int64
	Tracks        []TrackSpec
	LossBurst     int // size of one burst loss (packets)
	ReorderWindow int
	Duplicates    int // number of duplicate packets injected
	Reorders      int // number of in-window swaps injected
}

// Frame is one generated codec frame before packetization.
type Frame struct {
	Track     uint32
	Sample    uint32 // 1-based sample number
	Timestamp uint32 // RTP timestamp after wrap handling
	Keyframe  bool
	// Complete reports whether all its packets survived injection.
	Complete bool
	// Data is the Annex B / OBU byte stream. NAL frames carry a trailing
	// start code while complete per the codec package contract.
	Data []byte
}

// Fixture is the generated input set.
type Fixture struct {
	Frames  map[uint32][]*Frame // track SSRC -> frames in sample order
	Packets []*transport.Packet // impaired on-wire stream, arrival order
	// KeySamples maps SSRC -> set of complete keyframe sample numbers.
	KeySamples map[uint32]map[uint32]bool
	// TruthPackets is the original packet count before loss/duplication.
	TruthPackets int
	// BurstSeq records the lost burst per track as [first,last] sequence.
	BurstSeq map[uint32][2]uint16
}

// Build generates every track, packetizes and injects impairments
// deterministically from cfg.Seed.
func Build(cfg Config) *Fixture {
	rng := rand.New(rand.NewSource(cfg.Seed))
	fx := &Fixture{
		Frames:     map[uint32][]*Frame{},
		KeySamples: map[uint32]map[uint32]bool{},
		BurstSeq:   map[uint32][2]uint16{},
	}
	for _, spec := range cfg.Tracks {
		pkts, total := buildTrack(rng, fx, spec, cfg)
		fx.TruthPackets += total
		fx.Packets = append(fx.Packets, pkts...)
	}
	injectDuplicates(rng, fx, cfg.Duplicates)
	injectReorders(rng, fx, cfg.Reorders)
	return fx
}

func buildTrack(rng *rand.Rand, fx *Fixture, spec TrackSpec,
	cfg Config) (pkts []*transport.Packet, total int) {
	frames := make([]*Frame, 0, spec.Frames)
	keys := map[uint32]bool{}
	seq := spec.SeqStart
	ts := spec.TSStart
	wrapDone := false
	for i := 0; i < spec.Frames; i++ {
		sample := uint32(i + 1)
		isKey := i%spec.KeyEvery == 0
		if spec.TSWrap && !wrapDone && i == spec.Frames/2 {
			// Jump close to the 32-bit ceiling so the next increment wraps.
			ts = ^uint32(0) - spec.TSIncr + 1
			wrapDone = true
		}
		ts += spec.TSIncr
		data := synthFrame(spec.Codec, sample, isKey, true)
		fr := &Frame{Track: spec.SSRC, Sample: sample, Timestamp: ts,
			Keyframe: isKey, Complete: true, Data: data}
		frames = append(frames, fr)
		if isKey {
			keys[sample] = true
		}
		// Two fragments per frame; marker on the last.
		mid := len(data) / 2
		chunks := [2][]byte{data[:mid], data[mid:]}
		for j, chunk := range chunks {
			pkts = append(pkts, &transport.Packet{
				SSRC:      spec.SSRC,
				Seq:       seq,
				Timestamp: ts,
				Marker:    j == 1,
				Payload:   append([]byte(nil), chunk...),
			})
			seq++
		}
	}
	total = len(pkts)
	pkts = removeBurst(rng, pkts, frames, spec, cfg.LossBurst, fx)
	fx.Frames[spec.SSRC] = frames
	// Only surviving, complete keyframes may be referenced by an index.
	completeKeys := map[uint32]bool{}
	for _, fr := range frames {
		if fr.Keyframe && fr.Complete {
			completeKeys[fr.Sample] = true
		}
	}
	fx.KeySamples[spec.SSRC] = completeKeys
	return pkts, total
}

// removeBurst removes cfg.LossBurst consecutive packets at a deterministic
// position chosen to intersect a keyframe, marking that frame incomplete.
func removeBurst(rng *rand.Rand, pkts []*transport.Packet, frames []*Frame,
	spec TrackSpec, burst int, fx *Fixture) []*transport.Packet {
	if burst <= 0 {
		return pkts
	}
	// Aim at the second keyframe (sample index >=1) so the first keyframe
	// remains intact and hit rates stay measurable.
	keyIdx := secondKeyframeIndex(frames)
	targetPkt := (keyIdx + 1) * 2 // last fragment packet of that frame
	start := targetPkt - burst + 1
	if start < 0 {
		start = 0
	}
	if start+burst > len(pkts) {
		start = len(pkts) - burst
	}
	fx.BurstSeq[spec.SSRC] = [2]uint16{pkts[start].Seq, pkts[start+burst-1].Seq}
	for i := start; i < start+burst; i++ {
		fr := frames[i/2]
		fr.Complete = false
		delete(fx.KeySamples[spec.SSRC], fr.Sample)
		if i == start && i%2 == 0 {
			// First fragment lost: nothing of the frame can be trusted.
			fr.Data = fr.Data[:len(fr.Data)/2]
		}
	}
	copy(pkts[start:], pkts[start+burst:])
	return pkts[:len(pkts)-burst]
}

func secondKeyframeIndex(frames []*Frame) int {
	seen := 0
	for i, fr := range frames {
		if fr.Keyframe {
			seen++
			if seen == 2 {
				return i
			}
		}
	}
	return len(frames) - 1
}
