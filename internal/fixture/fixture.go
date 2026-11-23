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
	// BurstEnabled selects which single track carries the loss burst, so the
	// global loss stays exactly LossBurst packets while every track still
	// exercises reorder and duplicate handling.
	BurstEnabled bool
}

// Config controls the injected transmission impairments.
type Config struct {
	Seed          int64
	Tracks        []TrackSpec
	LossBurst     int // size of one burst loss (packets)
	ReorderWindow int
	Duplicates    int // number of duplicate packets injected
	Reorders      int // number of in-window swaps injected
	// Bandwidth enables the continuously varying link model. Arrival order is
	// perturbed by a deterministic saw-tooth congestion wave plus bursty extra
	// reordering, so the reorderer sees a shifting window instead of a fixed
	// one. Loss remains owned by LossBurst so conservation stays exact.
	Bandwidth bool
	// BandwidthDepth caps how far the congestion wave may delay a packet. It
	// must stay inside the reorder window so delayed packets are repairable
	// rather than becoming loss; zero disables the wave, leaving plain
	// duplicate/reorder/loss injection if Bandwidth is still true.
	BandwidthDepth int
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
	// CongestionPeak is the deepest congestion wave the link model reached;
	// zero when the bandwidth model is disabled.
	CongestionPeak int
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
	if cfg.Bandwidth {
		applyBandwidthModel(cfg.Seed, fx, cfg.BandwidthDepth)
	}
	injectReorders(cfg.Seed, fx, cfg.Reorders)
	injectDuplicates(cfg.Seed, fx, cfg.Duplicates)
	return fx
}

func buildTrack(rng *rand.Rand, fx *Fixture, spec TrackSpec,
	cfg Config) (pkts []*transport.Packet, total int) {
	frames := make([]*Frame, 0, spec.Frames)
	keys := map[uint32]bool{}
	seq := spec.SeqStart
	ts := spec.TSStart
	for i := 0; i < spec.Frames; i++ {
		sample := uint32(i + 1)
		isKey := i%spec.KeyEvery == 0
		// TSStart sits close enough to 2^32 that the increments cross zero
		// mid-stream. The wrap is therefore a genuine forward wrap; no
		// backward jump (which would model a node restart) is injected.
		ts += spec.TSIncr
		data := synthFrame(spec.Codec, sample, isKey, true)
		fr := &Frame{Track: spec.SSRC, Sample: sample, Timestamp: ts,
			Keyframe: isKey, Complete: true, Data: data}
		frames = append(frames, fr)
		if isKey {
			keys[sample] = true
		}
		// Packetize along codec unit boundaries so any surviving fragment is
		// itself a parseable unit. NAL frames split at their midpoint (Annex B
		// start codes stay self-resynchronizing) and AV1 frames split at the
		// OBU boundary; the marker bit closes the final fragment.
		chunks := fragment(spec.Codec, data)
		for j, chunk := range chunks {
			pkts = append(pkts, &transport.Packet{
				SSRC:      spec.SSRC,
				Seq:       seq,
				Timestamp: ts,
				Marker:    j == len(chunks)-1,
				Payload:   append([]byte(nil), chunk...),
			})
			seq++
		}
	}
	total = len(pkts)
	if spec.BurstEnabled {
		pkts = removeBurst(rng, pkts, frames, spec, cfg.LossBurst, fx)
	}
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

// removeBurst removes cfg.LossBurst consecutive packets positioned so the
// range always includes a keyframe's two fragments. It picks the second
// keyframe that leaves at least one surviving packet after the burst, so the
// reorderer always observes the hole. Only that keyframe is flagged
// incomplete; fixture truth data stays intact and the truncated bytes are
// reconstructed downstream from surviving packets.
func removeBurst(rng *rand.Rand, pkts []*transport.Packet, frames []*Frame,
	spec TrackSpec, burst int, fx *Fixture) []*transport.Packet {
	if burst <= 0 {
		return pkts
	}
	// Locate a keyframe whose full packet range fits with a packet to spare.
	keyIdx := -1
	keysSeen := 0
	for i, fr := range frames {
		if !fr.Keyframe {
			continue
		}
		keysSeen++
		markerPkt := (i + 1) * 2 // packet index of this frame's marker fragment
		if keysSeen >= 2 && markerPkt+1 < len(pkts) {
			keyIdx = i
			break
		}
		keyIdx = i // fall back to the last eligible keyframe if needed
	}
	if keyIdx < 0 {
		return pkts
	}
	firstFrag := keyIdx * 2 // first fragment packet of the target keyframe
	start := firstFrag
	if start+burst >= len(pkts) {
		start = len(pkts) - burst - 1
	}
	if start < 0 {
		start = 0
	}
	fx.BurstSeq[spec.SSRC] = [2]uint16{pkts[start].Seq, pkts[start+burst-1].Seq}
	// Flag exactly the target keyframe incomplete and drop it from the
	// complete-keyframe index set.
	frames[keyIdx].Complete = false
	delete(fx.KeySamples[spec.SSRC], frames[keyIdx].Sample)
	copy(pkts[start:], pkts[start+burst:])
	return pkts[:len(pkts)-burst]
}
