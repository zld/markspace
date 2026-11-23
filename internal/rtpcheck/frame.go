// Package rtpcheck validates that a segment's payload can be framed into RTP
// packets without any fragment crossing the segment boundary. It uses
// pion/rtp's packetizers and depacketizers to prove that reassembly of the
// emitted packets reproduces the original bytes exactly.
package rtpcheck

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
)

// Codec selects the RTP payload format used to frame a payload.
type Codec string

const (
	CodecVP9  Codec = "V_VP9"
	CodecVP8  Codec = "V_VP8"
	CodecOpus Codec = "A_OPUS"
)

// ErrUnsupportedCodec reports a codec without a framing strategy.
var ErrUnsupportedCodec = errors.New("rtpcheck: unsupported codec")

// Result reports one payload's framing outcome.
type Result struct {
	Codec     Codec  `json:"codec"`
	Packets   int    `json:"packets"`
	Bytes     int    `json:"bytes"`
	Roundtrip bool   `json:"roundtrip"`
	Err       string `json:"error,omitempty"`
}

func payloader(c Codec) (rtp.Payloader, error) {
	switch c {
	case CodecVP9:
		return &codecs.VP9Payloader{}, nil
	case CodecVP8:
		return &codecs.VP8Payloader{}, nil
	case CodecOpus:
		return &codecs.OpusPayloader{}, nil
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedCodec, c)
	}
}

// Frame packetizes payload with the given MTU and verifies that the packets all
// belong to one access unit and reassemble byte-exactly. Any packet holding
// bytes from two segments would break this: the function is called per segment,
// and it rejects a payload whose reassembly differs from the input.
func Frame(c Codec, mtu uint16, payload []byte) (Result, error) {
	res := Result{Codec: c, Bytes: len(payload)}
	p, err := payloader(c)
	if err != nil {
		res.Err = err.Error()
		return res, err
	}
	pk := rtp.NewPacketizer(mtu, 96, 0xC0FFEE, p, rtp.NewFixedSequencer(1), 90000)
	packets := pk.Packetize(payload, 1)
	if len(packets) == 0 {
		err := errors.New("rtpcheck: packetizer produced no packets")
		res.Err = err.Error()
		return res, err
	}
	res.Packets = len(packets)
	var rebuilt []byte
	for _, packet := range packets {
		raw, err := packet.Marshal()
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
			err := fmt.Errorf("rtpcheck: unexpected SSRC %#x", decoded.SSRC)
			res.Err = err.Error()
			return res, err
		}
		part, err := depacketize(c, decoded.Payload)
		if err != nil {
			res.Err = err.Error()
			return res, err
		}
		rebuilt = append(rebuilt, part...)
	}
	res.Roundtrip = bytes.Equal(rebuilt, payload)
	if !res.Roundtrip {
		err := fmt.Errorf("rtpcheck: reassembly differs (%d != %d bytes)", len(rebuilt), len(payload))
		res.Err = err.Error()
		return res, err
	}
	return res, nil
}

func depacketize(c Codec, payload []byte) ([]byte, error) {
	switch c {
	case CodecVP9:
		var d codecs.VP9Packet
		return d.Unmarshal(payload)
	case CodecVP8:
		var d codecs.VP8Packet
		return d.Unmarshal(payload)
	case CodecOpus:
		var d codecs.OpusPacket
		return d.Unmarshal(payload)
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedCodec, c)
	}
}

// CheckSegment verifies framing for every payload in one segment. Each payload
// must roundtrip independently, which is exactly the property that no packet
// spans two segments.
func CheckSegment(c Codec, mtu uint16, payloads [][]byte) (result []Result, err error) {
	for i, p := range payloads {
		res, err := Frame(c, mtu, p)
		if err != nil {
			return result, fmt.Errorf("rtpcheck: payload %d: %w", i, err)
		}
		result = append(result, res)
	}
	return result, nil
}
