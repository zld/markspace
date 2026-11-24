package fixture

import (
	"github.com/pion/rtp"
)

// Stream is a generated sample: raw MPEG-TS bytes plus, for RTP scenarios,
// the sequence of RTP packets that transport them.
type Stream struct {
	Spec        Spec
	TS          []byte
	RTP         []*rtp.Packet // nil for raw-TS scenarios
	SegmentWAVs [][]byte      // the per-segment WAV bodies embedded in the stream
}

// Build deterministically generates the full sample stream for a spec.
func Build(spec Spec) *Stream {
	b := newTSBuilder()
	var raw [][]byte
	var wavs [][]byte

	// PAT + PMT v0.
	pmtCC := b.nextCC(PMTPID)
	raw = append(raw, b.psiPackets(patSection(), pmtSection(0), pmtCC)...)

	for seg := 0; seg < spec.Segments; seg++ {
		// A version update halfway through swaps in PMT v1 (same PIDs).
		if spec.PSIUpdate && seg == spec.Segments/2 {
			newCC := b.nextCC(PMTPID)
			raw = append(raw, rawPacket(PMTPID, newCC, true, 0x01, nil,
				append([]byte{0x00}, padPayload(pmtSection(1))...)))
		}
		wav := makeSegmentWAV(seg)
		wavs = append(wavs, wav)
		pcr := pcrForSegment(seg, spec.WrapAtPCR)
		audio := b.audioPackets(seg, wav, pcr)
		raw = append(raw, audio...)

		if spec.CCGap && seg == 2 {
			applyCCDamage(b, &raw, audio)
		}
	}

	out := concatPackets(raw)
	stream := &Stream{Spec: spec, SegmentWAVs: wavs}
	if spec.Truncate > 0 && spec.Truncate < len(out) {
		out = out[:len(out)-spec.Truncate]
	}
	if spec.LossyRTP {
		stream.RTP = transportOverRTP(raw, spec.Seed)
		// TS bytes remain available for completeness; the engine consumes RTP.
	}
	stream.TS = out
	return stream
}

// applyCCDamage injects one duplicate (the final segment packet repeated with
// the same CC) and a continuity jump. The jump is made by advancing the
// builder's counter, so the next real packet simply carries a skipped CC value:
// no synthetic packet is inserted (which would itself consume a CC and cause a
// false duplicate), and no media bytes are invented.
func applyCCDamage(b *tsBuilder, raw *[][]byte, audio [][]byte) {
	dup := clonePacket(audio[len(audio)-1])
	*raw = append(*raw, dup) // identical CC -> flagged cc_dup and ignored
	// Skip two CC values; the following segment's first packet then lands on a
	// CC two ahead of the last real one, recording a two-packet cc_gap.
	b.cc[AudioPID] = (b.cc[AudioPID] + 2) & 0x0f
}

func clonePacket(p []byte) []byte {
	c := make([]byte, len(p))
	copy(c, p)
	return c
}

func concatPackets(packets [][]byte) []byte {
	n := 0
	for _, p := range packets {
		n += len(p)
	}
	out := make([]byte, 0, n)
	for _, p := range packets {
		out = append(out, p...)
	}
	return out
}

// transportOverRTP wraps each TS packet in one RTP packet, then applies a
// deterministic, bounded set of transport disorders: swaps (recoverable),
// duplicates and genuine drops. The reorder window must recover the swaps.
func transportOverRTP(packets [][]byte, seed int64) []*rtp.Packet {
	seq := uint16(seed)
	all := make([]*rtp.Packet, 0, len(packets))
	for _, payload := range packets {
		p := &rtp.Packet{
			Header: rtp.Header{
				Version: 2, PayloadType: 33, // MP2T payload type
				SequenceNumber: seq, Timestamp: uint32(seq) * 3000, SSRC: 7,
			},
			Payload: clonePacket(payload),
		}
		all = append(all, p)
		seq++
	}
	return disorder(all)
}

// disorder applies fixed, seed-independent damage so acceptance numbers stay
// reproducible: swap two adjacent pairs, duplicate one packet, drop two.
func disorder(in []*rtp.Packet) []*rtp.Packet {
	if len(in) < 12 {
		return in
	}
	// Recoverable reordering: swap packets 3<->4 and 7<->8 (within the window).
	in[3], in[4] = in[4], in[3]
	in[7], in[8] = in[8], in[7]
	// Duplicate packet 5 keeping its sequence number.
	dup := *in[5]
	dup.Payload = clonePacket(in[5].Payload)
	// Build output excluding two genuine drops (indices 10 and 14), plus dup.
	var out []*rtp.Packet
	drop := map[int]bool{10: true, 14: true}
	for i, p := range in {
		if i == 6 {
			out = append(out, &dup)
		}
		if drop[i] {
			continue
		}
		out = append(out, p)
	}
	return out
}
