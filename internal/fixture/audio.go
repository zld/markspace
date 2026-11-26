package fixture

import (
	"encoding/binary"

	pionrtp "github.com/pion/rtp"

	"replaykit/media/audio"
)

// AudioStream is a generated length-framed RTP source plus its task params.
type AudioStream struct {
	Name   string
	Params audio.Params
	Framed []byte
}

const fixtureSSRC uint32 = 0x52504C59 // "RPLY"

// frameRTP marshals one RTP packet and prefixes the 2-byte BE length.
func frameRTP(seq uint16, ts uint32, payload []byte) []byte {
	p := pionrtp.Packet{
		Header: pionrtp.Header{
			Version: 2, SequenceNumber: seq, Timestamp: ts, SSRC: fixtureSSRC, Marker: true,
		},
		Payload: payload,
	}
	raw, err := p.Marshal()
	if err != nil {
		panic(err)
	}
	out := make([]byte, 2+len(raw))
	binary.BigEndian.PutUint16(out, uint16(len(raw)))
	copy(out[2:], raw)
	return out
}

// opusPacket returns a 20 ms single-frame Opus payload (TOC config 1).
func opusPacket(seed byte) []byte { return []byte{0x08, seed, 0x00, 0xDE, 0xAD} }

// aacAUPayload builds one RFC 3640 AU with a 16-bit AU header.
func aacAUPayload(seed byte) []byte {
	au := []byte{seed, 0x11, 0x22, 0x33} // 4-byte AAC frame
	size := len(au)
	hdr1 := byte(size >> 5)
	hdr2 := byte((size & 0x1F) << 3)
	out := []byte{
		0x00, 0x10, // AU-headers-length = 16 bits
		hdr1, hdr2, // 13-bit AU size + flags
	}
	return append(out, au...)
}

// OpusClean returns a lossless, duplicate-free 20 ms Opus stream.
func OpusClean(packets int) AudioStream {
	var framed []byte
	for i := 0; i < packets; i++ {
		framed = append(framed, frameRTP(uint16(i), uint32(i*960), opusPacket(byte(i)))...)
	}
	return AudioStream{
		Name:   "opus-clean",
		Params: audio.Params{Codec: audio.CodecOpus, SampleRate: 48000, Channels: 2},
		Framed: framed,
	}
}

// AACClean returns a lossless AAC stream using in-band 16-bit AU headers.
func AACClean(packets int) AudioStream {
	var framed []byte
	for i := 0; i < packets; i++ {
		framed = append(framed, frameRTP(uint16(i), uint32(i*1024), aacAUPayload(byte(i)))...)
	}
	return AudioStream{
		Name: "aac-clean",
		Params: audio.Params{
			Codec: audio.CodecAAC, SampleRate: 48000, Channels: 2,
			AAC: audio.AACConfig{AUHeaderBits: 16, SamplesPerFrame: 1024, SampleRate: 48000, ObjectType: 2},
		},
		Framed: framed,
	}
}

// LossDup returns an Opus arrival sequence 0,1,1,2,4,5: sequence 3 is lost
// exactly once and sequence 1 is duplicated exactly once.
func LossDup() AudioStream {
	seqs := []uint16{0, 1, 1, 2, 4, 5}
	var framed []byte
	for arrival, seq := range seqs {
		framed = append(framed, frameRTP(seq, uint32(seq)*960, opusPacket(byte(arrival)))...)
	}
	return AudioStream{
		Name:   "opus-loss-dup",
		Params: audio.Params{Codec: audio.CodecOpus, SampleRate: 48000, Channels: 1},
		Framed: framed,
	}
}
