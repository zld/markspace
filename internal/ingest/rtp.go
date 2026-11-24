package ingest

import (
	"errors"

	"github.com/pion/rtp"
)

// RTPPacket is a parsed RTP frame plus its raw stream location.
type RTPPacket struct {
	Packet *rtp.Packet
	Offset int64 // offset of the framing tag in the raw stream
}

// ParseRTP decodes one RTP payload (RFC 3550 section 5.1). Validation beyond
// pion's own checks is mapped onto the stable error-code taxonomy.
func ParseRTP(f *Frame) (*RTPPacket, error) {
	if f.Type != TagRTP {
		return nil, &ParseError{Code: ErrUnknownFrame, Offset: f.FrameOffset, Stage: StageParse,
			Err: errors.New("frame is not tagged RTP")}
	}
	p := &rtp.Packet{}
	if err := p.Unmarshal(f.Payload); err != nil {
		return nil, &ParseError{Code: ErrRTPHeader, Offset: f.FrameOffset, Stage: StageParse, Err: err}
	}
	if p.Version != 2 {
		return nil, &ParseError{Code: ErrRTPHeader, Offset: f.FrameOffset, Stage: StageParse,
			Err: errors.New("unsupported RTP version")}
	}
	return &RTPPacket{Packet: p, Offset: f.FrameOffset}, nil
}
