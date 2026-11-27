package rtpreplay

import (
	"encoding/hex"
	"math/rand"
)

func hexEncode(b []byte) string { return hex.EncodeToString(b) }

// opusFixtureMP4 renders the companion MP4 for an Opus case with the given
// per-packet durations in ticks at 48 kHz.
func opusFixtureMP4(durations []uint32, sizes []uint32) ([]byte, error) {
	return WriteMP4(AudioTrackSpec{
		Codec: CodecOpus, Timescale: 48000, Durations: durations, Sizes: sizes,
		OpusPreSkip: 312, OpusRate: 48000, SampleRate: 48000, Channels: 2,
	})
}

func attachMP4(fx *Fixture, durations []uint32, sizes []uint32) error {
	mp4, err := opusFixtureMP4(durations, sizes)
	if err != nil {
		return err
	}
	fx.MP4Bytes = mp4
	fx.MP4Name = "opus-" + fx.Case + ".mp4"
	fx.Trace.MP4 = &MP4Ref{FileName: fx.MP4Name, Sha256: sha256Hex(mp4)}
	return nil
}

// generateWrapReorder crosses the 16-bit sequence wrap and delivers a block of
// packets out of order after the wrap point.
func generateWrapReorder(seed int64) (*Fixture, error) {
	rng := rand.New(rand.NewSource(seed))
	const n = 40
	var durs, sizes []uint32
	for i := 0; i < n; i++ {
		payload := opusFrame20ms(rng, 40+rng.Intn(10))
		durs = append(durs, 960)
		sizes = append(sizes, uint32(len(payload)))
	}
	tr := simpleTrace(seed, uint64(n*20))
	baseSeq := uint16(65516) // wraps after 20 packets
	deliver := make([]int, 0, n)
	for i := 0; i < n; i++ {
		deliver = append(deliver, i)
	}
	// Swap three adjacent pairs just after the wrap to force reordering.
	for _, i := range []int{21, 25, 30} {
		deliver[i], deliver[i+1] = deliver[i+1], deliver[i]
	}
	for idx, i := range deliver {
		payload := opusFrame20ms(rng, 40)
		tr.Streams[0].Packets = append(tr.Streams[0].Packets, TracePacket{
			RTPHex:    rtpHex(nil, baseSeq+uint16(i), uint32(i*960), i == n-1, payload, 111),
			ArrivalMs: int64(20 + idx*20),
		})
	}
	fx := &Fixture{Case: CaseWrapReorder, Seed: seed, Trace: tr}
	if err := attachMP4(fx, durs, sizes); err != nil {
		return nil, err
	}
	fx.Notes = append(fx.Notes, "40 packets starting at seq 65516: sequence wrap plus three swapped pairs; zero loss expected")
	return fx, nil
}

// generateDuplicate repeats selected packets (one before its original, one
// long after) and reorders one pair; duplicates must not be counted twice.
func generateDuplicate(seed int64) (*Fixture, error) {
	rng := rand.New(rand.NewSource(seed))
	const n = 30
	var durs, sizes []uint32
	tr := simpleTrace(seed, uint64(n*20))
	for i := 0; i < n; i++ {
		payload := opusFrame20ms(rng, 60)
		durs = append(durs, 960)
		sizes = append(sizes, uint32(len(payload)))
	}
	emit := func(seqIdx int, arrivalMs int64, dup bool) {
		fault := ""
		if dup {
			fault = "duplicate"
		}
		payload := opusFrame20ms(rand.New(rand.NewSource(int64(seqIdx)+1)), 60)
		tr.Streams[0].Packets = append(tr.Streams[0].Packets, TracePacket{
			RTPHex:    rtpHex(nil, uint16(seqIdx+1), uint32(seqIdx*960), false, payload, 111),
			ArrivalMs: arrivalMs,
			Fault:     fault,
		})
	}
	at := int64(20)
	for i := 0; i < n; i++ {
		emit(i, at, false)
		at += 20
		if i == 5 || i == 12 || i == 20 {
			emit(i, at, true) // immediate duplicate
			at += 5
		}
	}
	// A duplicate that arrives after its slot already closed.
	emit(3, at, true)
	fx := &Fixture{Case: CaseDuplicate, Seed: seed, Trace: tr}
	if err := attachMP4(fx, durs, sizes); err != nil {
		return nil, err
	}
	fx.Notes = append(fx.Notes, "four duplicate arrivals (three in-window, one late); duplicates must not add media time")
	return fx, nil
}

// generateBurstLoss drops two consecutive runs of packets and leaves holes in
// the arrival timeline that the media clock must not paper over.
func generateBurstLoss(seed int64) (*Fixture, error) {
	rng := rand.New(rand.NewSource(seed))
	const n = 60
	drop := func(i int) bool {
		return (i >= 10 && i < 18) || (i >= 40 && i < 44)
	}
	var durs, sizes []uint32
	tr := simpleTrace(seed, uint64(n*20))
	at := int64(20)
	for i := 0; i < n; i++ {
		payload := opusFrame20ms(rng, 50)
		durs = append(durs, 960)
		sizes = append(sizes, uint32(len(payload)))
		if drop(i) {
			continue
		}
		tr.Streams[0].Packets = append(tr.Streams[0].Packets, TracePacket{
			RTPHex:    rtpHex(nil, uint16(i+1), uint32(i*960), false, payload, 111),
			ArrivalMs: at,
		})
		at += 20
	}
	fx := &Fixture{Case: CaseBurstLoss, Seed: seed, Trace: tr}
	if err := attachMP4(fx, durs, sizes); err != nil {
		return nil, err
	}
	fx.Notes = append(fx.Notes,
		"60-packet stream with an 8-packet burst (seq 11-18) and a 4-packet burst (seq 41-44) missing",
		"RTP timestamps still advance across the holes: the manifest must report gaps without inserting audio")
	return fx, nil
}
