package rtpreplay

import (
	"math/rand"
)

// aacAUHeaderSizeLength and indexLength configure the generated RFC 3640 AU
// header. 13 bits of size + 3 bits of index is a common 2-byte header.
const (
	aacAUHeaderSizeLength  = 13
	aacAUHeaderIndexLength = 3
)

// aacPayload builds one mpeg4-generic RTP payload: AU-headers-length, one AU
// header, then the AU bytes. The AU duration itself comes from the
// AudioSpecificConfig (1024 samples), not from this header.
func aacPayload(auIndex uint16, au []byte) []byte {
	const headerBits = aacAUHeaderSizeLength + aacAUHeaderIndexLength
	out := make([]byte, 2, 2+headerBits/8+len(au))
	out[0] = 0
	out[1] = headerBits // AU-headers-length in bits
	// 16-bit field: 13-bit size then 3-bit index.
	v := uint16(len(au))<<aacAUHeaderIndexLength | auIndex&0x7
	out = append(out, byte(v>>8), byte(v))
	return append(out, au...)
}

// buildAACStream adds an AAC-LC 48 kHz track whose AU durations are 1024
// ticks and whose AU count matches the companion MP4 sample table.
func buildAACStream(rng *rand.Rand, events []TimedEvent, n int, startSeq uint16) (*Stream, []uint32, []uint32) {
	cfg := &AACStreamConfig{
		UseAUHeaders: true,
		SizeLength:   aacAUHeaderSizeLength,
		IndexLength:  aacAUHeaderIndexLength,
		AACConfig: AACConfig{
			AudioObjectType: 2,
			SampleRate:      48000,
			ChannelConfig:   2,
		},
	}
	st := &Stream{
		Track: "aac-music", Codec: CodecAAC, ClockHz: 48000, AAC: cfg,
	}
	var durs, sizes []uint32
	for i := 0; i < n; i++ {
		// 1024 samples at 48 kHz is 21.333 ms of media per AU.
		mediaMs := int64(i) * 1024 * 1000 / 48000
		size := aacPayloadBytesFor(targetBpsAt(events, mediaMs))
		au := make([]byte, size)
		au[0] = 0x21 // arbitrary AAC raw block header byte
		for j := 1; j < len(au); j++ {
			au[j] = byte(rng.Intn(256))
		}
		st.Packets = append(st.Packets, TracePacket{
			RTPHex:    rtpHex(startSeq+uint16(i), uint32(i*1024), false, aacPayload(uint16(i%8), au), 102),
			ArrivalMs: baseDelayMs + mediaMs + int64(rng.Intn(jitterMs)),
		})
		durs = append(durs, 1024)
		sizes = append(sizes, uint32(len(au)))
	}
	st.DeclaredDurationMs = uint64(n * 1024 * 1000 / 48000)
	return st, durs, sizes
}
