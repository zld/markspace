// Package fixture generates fully synthetic, seed-driven investigation
// traces: RTP packet records with Opus/AAC payloads, network impairments
// (duplicates, reorder, burst loss, truncation) and a matching MP4 track.
// No external sample files are referenced; every byte is produced in code.
package fixture

import (
	"errors"
	"math/rand"

	"tracecheck/internal/codec"
	"tracecheck/internal/merge"
	"tracecheck/internal/model"
	"tracecheck/internal/mp4"
)

// ErrUnknownProfile is returned when the requested profile does not exist.
var ErrUnknownProfile = errors.New("unknown fixture profile")

// Profile names accepted by the command line.
const (
	ProfileClean   = "clean"
	ProfileDamaged = "damaged"
	ProfileCancel  = "cancel"
)

// StreamID and track identifiers used by every generated trace.
const (
	OpusStream uint32 = 100
	AACStream  uint32 = 200
	VideoTrack uint32 = 1
)

// Trace is one generated investigation input plus the damage markers the
// generator intentionally applied, used by tests and the CLI alike.
type Trace struct {
	Profile string
	Seed    int64
	Packets []model.PacketRecord
	MP4     []byte
	// FlowSamples back the throughput windows; one entry per packet.
	FlowSamples []merge.ByteSample
	// Applied damage for assertions.
	Duplicates  int
	BurstLosses int
	Truncated   int
	// Cancel profile only: total jobs the producer would submit.
	PlannedJobs int
}

// Options controls fixture generation.
type Options struct {
	Profile    string
	Seed       int64
	CancelAfter int
}

// opusPacket builds a minimal valid one-frame Opus packet of 20 ms.
func opusPacket(rng *rand.Rand, config byte) []byte {
	toc := config<<3 | 0x00 // code 0: one frame
	payload := make([]byte, 2+rng.Intn(6))
	payload[0] = toc
	for i := 1; i < len(payload); i++ {
		payload[i] = byte(rng.Intn(256))
	}
	return payload
}

// aacPacket builds a minimal valid ADTS AAC frame at 44.1 kHz.
func aacPacket(rng *rand.Rand, rawLen int) []byte {
	raw := make([]byte, rawLen)
	for i := range raw {
		raw[i] = byte(rng.Intn(256))
	}
	return codec.BuildADTSForFixture(4, 2, raw)
}

// Generate produces the trace for the requested profile.
func Generate(opt Options) (*Trace, error) {
	rng := rand.New(rand.NewSource(opt.Seed))
	tr := &Trace{Profile: opt.Profile, Seed: opt.Seed}

	const packetCount = 24
	tr.PlannedJobs = packetCount
	tr.Packets = make([]model.PacketRecord, 0, packetCount+4)

	// Arrival schedule: 20 ms packets on the microsecond clock. Each RTP
	// stream has its own independent 16-bit sequence and timestamp space.
	var arrival model.Tick
	seqByStream := map[uint32]uint16{}
	tsByStream := map[uint32]uint32{}
	for i := 0; i < packetCount; i++ {
		isOpus := i%2 == 0
		var payload []byte
		var stream uint32
		var kind model.CodecKind
		if isOpus {
			payload = opusPacket(rng, 3) // 20 ms SILK
			stream, kind = OpusStream, model.CodecOpus
		} else {
			payload = aacPacket(rng, 16)
			stream, kind = AACStream, model.CodecAAC
		}
		seq := seqByStream[stream]
		rtpTS := tsByStream[stream]
		tr.Packets = append(tr.Packets, model.PacketRecord{
			Stream:       stream,
			Codec:        kind,
			Sequence:     seq,
			RTPTimestamp: rtpTS,
			Payload:      payload,
			ArrivalTick:  arrival,
			ClockRate:    48000,
		})
		tr.FlowSamples = append(tr.FlowSamples, merge.ByteSample{
			At: arrival, Bytes: int64(len(payload)),
		})
		arrival += 20_000
		seqByStream[stream] = seq + 1
		if isOpus {
			tsByStream[stream] = rtpTS + 960
		} else {
			tsByStream[stream] = rtpTS + 1024
		}
	}

	// Video track: 10 samples, 100 ms each in a 1000-unit timescale.
	// Video track: 10 samples at 1 ms each in a 1000-unit timescale, so a
	// healthy keyframe interval is 2 ms, inside the default 3 ms budget.
	spec := mp4.VideoSpec{
		TrackID:       VideoTrack,
		Timescale:     1000,
		SampleCounts:  []uint32{10},
		SampleDeltas:  []uint32{1},
		SampleSizes:   repeatU32(40, 10),
		KeyframeIndex: []uint32{1, 3, 5, 7, 9},
	}

	switch opt.Profile {
	case ProfileClean:
		// No impairments.
	case ProfileDamaged:
		applyDamaged(rng, tr, &spec)
	case ProfileCancel:
		applyCancelShape(rng, tr, &spec, opt.CancelAfter)
	default:
		return nil, ErrUnknownProfile
	}

	data, err := mp4.BuildVideoMP4(spec)
	if err != nil {
		return nil, err
	}
	tr.MP4 = data
	return tr, nil
}

// applyDamaged injects the exact impairments the acceptance criteria check:
// one duplicate, a three-packet burst loss, one truncated packet and a bad
// MP4 index, plus a throughput dip and an oversized keyframe gap.
func applyDamaged(rng *rand.Rand, tr *Trace, spec *mp4.VideoSpec) {
	// 1. Burst loss first, while packet indices still map 1:1 to the
	// generated sequence. Remove three same-stream records (even indices
	// are Opus) so the reorder buffer reports one three-number gap rather
	// than two unrelated single-number holes across streams.
	tr.Packets = removeRange(tr.Packets, 16, 1)
	tr.Packets = removeRange(tr.Packets, 14, 1)
	tr.Packets = removeRange(tr.Packets, 12, 1)
	tr.BurstLosses = 3

	// 2. Duplicate an early packet; the reorder buffer counts it once.
	dup := tr.Packets[2]
	tr.Packets = insertAt(tr.Packets, 4, dup)
	tr.Duplicates++

	// 3. Truncate one remaining Opus packet to an empty payload.
	for i := range tr.Packets {
		if tr.Packets[i].Codec == model.CodecOpus {
			tr.Packets[i].Payload = tr.Packets[i].Payload[:0]
			tr.Truncated++
			break
		}
	}

	// 4. Throughput dip: shrink the later flow samples to simulate a
	// bandwidth change distinct from packet duration.
	for i := len(tr.FlowSamples) / 2; i < len(tr.FlowSamples); i++ {
		tr.FlowSamples[i].Bytes = 1
	}

	// 5. MP4 damage: an out-of-range sync sample and a long keyframe gap.
	spec.BadSTSSIndex = 999
	// Keep only the boundary keyframes: the 1 -> 9 gap spans 8 ms, well
	// beyond the default 3 ms keyframe budget.
	spec.KeyframeIndex = []uint32{1, 9}
}

// applyCancelShape keeps the stream structurally clean; the cancellation
// behavior itself is driven by the worker pool and CancelAfter.
func applyCancelShape(rng *rand.Rand, tr *Trace, spec *mp4.VideoSpec, cancelAfter int) {
	if cancelAfter > 0 {
		tr.PlannedJobs = cancelAfter * 3
	}
}

func insertAt(packets []model.PacketRecord, at int, p model.PacketRecord) []model.PacketRecord {
	out := make([]model.PacketRecord, 0, len(packets)+1)
	out = append(out, packets[:at]...)
	out = append(out, p)
	out = append(out, packets[at:]...)
	return out
}

func removeRange(packets []model.PacketRecord, at, n int) []model.PacketRecord {
	return append(packets[:at], packets[at+n:]...)
}

func repeatU32(v, n uint32) []uint32 {
	out := make([]uint32, n)
	for i := range out {
		out[i] = v
	}
	return out
}
