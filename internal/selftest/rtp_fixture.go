package selftest

import (
	"fmt"

	"mediacore/internal/scan"
)

const (
	videoSSRC uint32 = 0x1111_0001
	audioSSRC uint32 = 0x2222_0002
)

func buildLossyRTPSpec() scan.RTPFixtureSpec {
	streams := []scan.RTPStreamMeta{
		{SSRC: videoSSRC, TrackID: 1, Codec: "mcrv", Kind: "video", Timescale: 90000},
		{SSRC: audioSSRC, TrackID: 2, Codec: "mcau", Kind: "audio", Timescale: 48000, Channels: 2, SampleRate: 48000},
	}

	var ordered []scan.RTPPacketSpec
	for seq := 0; seq < 24; seq++ {
		if seq >= 10 && seq <= 14 { // contiguous/burst loss
			continue
		}
		ordered = append(ordered, scan.RTPPacketSpec{
			SSRC: videoSSRC, PayloadTS: 96, Sequence: uint16(seq),
			Timestamp: uint32(seq) * 3750, Marker: true,
			Data: deterministicPayload("V", seq, 192),
		})
	}
	for seq := 0; seq < 50; seq++ {
		if seq >= 20 && seq <= 24 { // second burst
			continue
		}
		ordered = append(ordered, scan.RTPPacketSpec{
			SSRC: audioSSRC, PayloadTS: 97, Sequence: uint16(seq),
			Timestamp: uint32(seq) * 960, Marker: true,
			Data: deterministicPayload("A", seq, 256),
		})
	}

	// Reverse each SSRC block's arrival order deliberately. The slice currently
	// is video ascending then audio ascending; reverse the whole thing gives
	// audio descending followed by video descending, still heavily reordered.
	arrival := make([]scan.RTPPacketSpec, 0, len(ordered)+4)
	for i := len(ordered) - 1; i >= 0; i-- {
		arrival = append(arrival, ordered[i])
	}

	// Four duplicate transmissions after the originals have arrived.
	arrival = append(arrival,
		mustFindPacket(ordered, videoSSRC, 2),
		mustFindPacket(ordered, videoSSRC, 2),
		mustFindPacket(ordered, audioSSRC, 5),
		mustFindPacket(ordered, audioSSRC, 5),
	)
	return scan.RTPFixtureSpec{Streams: streams, Packets: arrival}
}

func mustFindPacket(ps []scan.RTPPacketSpec, ssrc uint32, seq uint16) scan.RTPPacketSpec {
	for _, p := range ps {
		if p.SSRC == ssrc && p.Sequence == seq {
			return p
		}
	}
	panic(fmt.Sprintf("missing fixture packet ssrc=%x seq=%d", ssrc, seq))
}

func deterministicPayload(prefix string, seq, n int) []byte {
	b := make([]byte, n)
	copy(b, prefix)
	for i := len(prefix); i < n; i++ {
		b[i] = byte((i*17 + seq*31) & 0xff)
	}
	return b
}
