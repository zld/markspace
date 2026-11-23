package rtpcheck

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/pion/rtp"
)

// ErrPayloadTooShort reports a payload that cannot carry a frame header.
var ErrPayloadTooShort = errors.New("rtpcheck: payload too short")

// FrameFor validates that a segment payload can be framed into RTP packets and
// reassembled byte-exactly. It is the boundary guarantee: the framing runs per
// segment, so reassembly equality proves no RTP packet mixed bytes from two
// segments. codec names the payload format; use CodecVP9/VP8/Opus for real
// bitstreams, anything else (or an empty string) for structural framing.
func FrameFor(codec string, mtu uint16, payload []byte) (Result, error) {
	if c, ok := knownCodec(codec); ok {
		return Frame(c, mtu, payload)
	}
	return FrameGeneric(mtu, payload)
}

// knownCodec maps codec IDs to pion payloaders. "V_VP9" only qualifies when the
// caller opts in via CodecVP9, because synthetic payloads may lack a VP9
// header; callers with real bitstreams pass the short codec name.
func knownCodec(codec string) (Codec, bool) {
	switch Codec(codec) {
	case CodecVP9, CodecVP8, CodecOpus:
		return Codec(codec), true
	default:
		return "", false
	}
}

// FrameGeneric fragments a payload into RTP packets and verifies byte-exact
// reassembly. It carries no codec semantics; it exists so arbitrary segment
// bytes can still be checked for boundary isolation.
func FrameGeneric(mtu uint16, payload []byte) (Result, error) {
	res := Result{Bytes: len(payload)}
	if mtu < 12 || len(payload) == 0 {
		return res, ErrPayloadTooShort
	}
	maxFragment := int(mtu) - 12
	seq := uint16(1)
	ts := uint32(90_000)
	var packets []*rtp.Packet
	fragments := [][]byte{}
	for off := 0; off < len(payload); off += maxFragment {
		end := off + maxFragment
		if end > len(payload) {
			end = len(payload)
		}
		frag := payload[off:end]
		fragments = append(fragments, frag)
		packets = append(packets, &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    96,
				SequenceNumber: seq,
				Timestamp:      ts,
				SSRC:           0xC0FFEE,
				Marker:         end == len(payload),
			},
			Payload: frag,
		})
		seq++
	}
	res.Packets = len(packets)
	var rebuilt []byte
	for _, p := range packets {
		raw, err := p.Marshal()
		if err != nil {
			res.Err = err.Error()
			return res, err
		}
		var decoded rtp.Packet
		if err := decoded.Unmarshal(raw); err != nil {
			res.Err = err.Error()
			return res, err
		}
		if decoded.SSRC != 0xC0FFEE {
			err := fmt.Errorf("rtpcheck: SSRC changed in flight")
			res.Err = err.Error()
			return res, err
		}
		rebuilt = append(rebuilt, decoded.Payload...)
	}
	res.Roundtrip = bytes.Equal(rebuilt, payload)
	if !res.Roundtrip {
		err := fmt.Errorf("rtpcheck: reassembly differs (%d != %d bytes)", len(rebuilt), len(payload))
		res.Err = err.Error()
		return res, err
	}
	return res, nil
}
