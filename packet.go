package mediakernel

import (
	"encoding/binary"
	"errors"
	"io"

	"github.com/pion/rtp"
)

// Codec identifies the packet codec. Both codecs run on the shared 48 kHz (or
// caller-supplied) media clock after sample-count normalization.
type Codec uint8

const (
	CodecUnknown Codec = 0
	CodecOpus    Codec = 1
	CodecAAC     Codec = 2
)

// String renders the codec for JSON reports.
func (c Codec) String() string {
	switch c {
	case CodecOpus:
		return "opus"
	case CodecAAC:
		return "aac"
	default:
		return "unknown"
	}
}

// MarshalJSON emits the codec as a stable string token.
func (c Codec) MarshalJSON() ([]byte, error) {
	return []byte(`"` + c.String() + `"`), nil
}

// Valid frame sizes in samples per packet. Opus at 48 kHz allows a fixed set of
// frame durations; AAC raw frames carry 1024 samples (960 for HE-AAC).
var (
	opusValidSamples = map[uint32]bool{120: true, 240: true, 480: true, 960: true, 1920: true, 2880: true}
	aacValidSamples  = map[uint32]bool{960: true, 1024: true}
)

const (
	defaultOpusSamples = 960
	defaultAACSamples  = 1024
	frameSync          = 0xA5
	// FlagTruncated marks a deliberately corrupted fixture frame.
	FlagTruncated uint8 = 1 << 0
	// rtpMaxFrameLen bounds the 16-bit length-prefixed fixture framing.
	rtpMaxFrameLen = 65535
)

// Packet is one parsed media packet positioned at a byte offset in the input.
type Packet struct {
	Seq             uint16 `json:"seq"`
	Codec           Codec  `json:"codec"`
	Offset          int64  `json:"offset"`
	ValidSamples    int64  `json:"valid_samples"`
	DeclaredSamples uint32 `json:"declared_samples"`
	PayloadLen      int    `json:"payload_len"`
}

// ValidSamplesFor validates a declared per-packet sample count for the codec
// and returns it as int64, or false when the duration is not a legal frame.
func ValidSamplesFor(codec Codec, samples uint32) (int64, bool) {
	switch codec {
	case CodecOpus:
		return int64(samples), opusValidSamples[samples]
	case CodecAAC:
		return int64(samples), aacValidSamples[samples]
	default:
		return 0, false
	}
}

// DefaultGapSamples returns the canonical assumed sample count of one lost
// frame. It only sizes the reserved gap range; no audio content is synthesized.
func DefaultGapSamples(codec Codec) int64 {
	switch codec {
	case CodecOpus:
		return defaultOpusSamples
	case CodecAAC:
		return defaultAACSamples
	default:
		return defaultOpusSamples
	}
}

// codecFrame is the inner payload carried inside each RTP packet:
//
//	byte 0      codec id
//	byte 1      flags (FlagTruncated, ...)
//	bytes 2..5  declared samples, big-endian uint32
//	bytes 6..   opaque audio payload
type codecFrame struct {
	codec   Codec
	flags   uint8
	samples uint32
	payload []byte
}

// BuildFramedPacket constructs one length-prefixed RTP packet for fixtures.
// The 2-byte big-endian length prefix is followed by an RTP packet whose
// payload is a codecFrame. Returned bytes are self-contained and deterministic.
func BuildFramedPacket(seq uint16, codec Codec, samples uint32, audioPayload []byte, flags uint8) ([]byte, error) {
	if len(audioPayload)+6 > rtpMaxFrameLen {
		return nil, NewError(PhaseParse, -1, "frame exceeds 16-bit framing limit")
	}
	inner := make([]byte, 6+len(audioPayload))
	inner[0] = byte(codec)
	inner[1] = flags
	binary.BigEndian.PutUint32(inner[2:6], samples)
	copy(inner[6:], audioPayload)
	pkt := &rtp.Packet{
		Header:  rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: seq, Marker: true},
		Payload: inner,
	}
	rtpBytes, err := pkt.Marshal()
	if err != nil {
		return nil, NewError(PhaseParse, -1, "rtp marshal failed").WithCause(err)
	}
	if len(rtpBytes) > rtpMaxFrameLen {
		return nil, NewError(PhaseParse, -1, "rtp packet exceeds framing limit")
	}
	out := make([]byte, 2+len(rtpBytes))
	binary.BigEndian.PutUint16(out, uint16(len(rtpBytes)))
	copy(out[2:], rtpBytes)
	return out, nil
}

// FrameReader parses length-prefixed RTP packets from a stream. Every reported
// error is a *MediaError carrying the byte offset of the offending frame.
type FrameReader struct {
	r         io.Reader
	offset    int64
	lenBuf    [2]byte
	headerBuf [4]byte
}

// NewFrameReader wraps r as a packet stream.
func NewFrameReader(r io.Reader) *FrameReader { return &FrameReader{r: r} }

// Offset returns the number of bytes consumed so far.
func (f *FrameReader) Offset() int64 { return f.offset }

// ReadPacket parses the next packet. io.EOF is returned unwrapped at a clean
// stream end; every other failure is a structured *MediaError.
func (f *FrameReader) ReadPacket() (*Packet, error) {
	frameStart := f.offset
	if _, err := io.ReadFull(f.r, f.lenBuf[:]); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, io.EOF // clean end between frames
		}
		return nil, NewError(PhaseParse, frameStart, "truncated length prefix").WithCause(err)
	}
	f.offset += 2
	frameLen := int(binary.BigEndian.Uint16(f.lenBuf[:]))
	buf := make([]byte, frameLen)
	if _, err := io.ReadFull(f.r, buf); err != nil {
		return nil, NewError(PhaseParse, frameStart, "truncated packet body").WithCause(err)
	}
	f.offset += int64(frameLen)

	var rtpPkt rtp.Packet
	if err := rtpPkt.Unmarshal(buf); err != nil {
		return nil, NewError(PhaseParse, frameStart, "invalid rtp header").WithSeq(0).WithCause(err)
	}
	frame, err := parseCodecFrame(rtpPkt.Payload, frameStart, rtpPkt.SequenceNumber)
	if err != nil {
		return nil, err
	}
	pkt := &Packet{
		Seq:             rtpPkt.SequenceNumber,
		Codec:           frame.codec,
		Offset:          frameStart,
		DeclaredSamples: frame.samples,
		PayloadLen:      len(frame.payload),
	}
	valid, ok := ValidSamplesFor(frame.codec, frame.samples)
	if !ok {
		return nil, NewError(PhaseParse, frameStart, "invalid frame duration").
			WithSeq(rtpPkt.SequenceNumber)
	}
	pkt.ValidSamples = valid
	return pkt, nil
}

func parseCodecFrame(payload []byte, offset int64, seq uint16) (*codecFrame, error) {
	if len(payload) < 6 {
		return nil, NewError(PhaseParse, offset, "truncated codec frame").WithSeq(seq)
	}
	cf := &codecFrame{
		codec:   Codec(payload[0]),
		flags:   payload[1],
		samples: binary.BigEndian.Uint32(payload[2:6]),
		payload: payload[6:],
	}
	if cf.codec != CodecOpus && cf.codec != CodecAAC {
		return nil, NewError(PhaseParse, offset, "unknown codec id").WithSeq(seq)
	}
	if cf.flags&FlagTruncated != 0 {
		return nil, NewError(PhaseParse, offset, "truncated packet flagged by source").WithSeq(seq)
	}
	return cf, nil
}

// ReadAll drains the reader into a slice, stopping at io.EOF. It exists for
// tests and small fixtures; the production pipeline streams packet by packet.
func ReadAll(r *FrameReader) ([]*Packet, error) {
	var out []*Packet
	for {
		pkt, err := r.ReadPacket()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		out = append(out, pkt)
	}
}
