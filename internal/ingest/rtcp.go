package ingest

import (
	"encoding/binary"
	"errors"
)

// RTCP packet types handled by the minimal parser (RFC 3550 section 6).
const (
	typeSenderReport byte = 200
)

// SenderReport is the subset of an RTCP SR block the clock layer consumes.
type SenderReport struct {
	SSRC        uint32
	NTPTime     uint64 // 64-bit NTP timestamp from the SR
	RTPTime     uint32 // RTP timestamp paired with NTPTime
	PacketCount uint32
	OctetCount  uint32
	Offset      int64  // raw stream offset of the framing tag
}

// RTCPPacket holds anything decoded from a compound RTCP frame. Only SR
// blocks are extracted; other report types are validated but ignored.
type RTCPPacket struct {
	Reports []SenderReport
	Offset  int64
}

// ParseRTCP walks a compound RTCP frame and extracts sender reports. A
// malformed header yields a *ParseError carrying the frame's raw offset.
func ParseRTCP(f *Frame) (*RTCPPacket, error) {
	if f.Type != TagRTCP {
		return nil, &ParseError{Code: ErrUnknownFrame, Offset: f.FrameOffset, Stage: StageParse,
			Err: errors.New("frame is not tagged RTCP")}
	}
	buf := f.Payload
	out := &RTCPPacket{Offset: f.FrameOffset}
	for len(buf) > 0 {
		if len(buf) < 4 {
			return nil, &ParseError{Code: ErrRTCPHeader, Offset: f.FrameOffset, Stage: StageParse,
				Err: errors.New("RTCP block shorter than 4 bytes")}
		}
		version := buf[0] >> 6
		if version != 2 {
			return nil, &ParseError{Code: ErrRTCPHeader, Offset: f.FrameOffset, Stage: StageParse,
				Err: errors.New("unsupported RTCP version")}
		}
		pType := buf[1]
		rc := int(buf[0] & 0x0f)
		declared := int(binary.BigEndian.Uint16(buf[2:4]))*4 + 4
		if declared > len(buf) || declared < 4 {
			return nil, &ParseError{Code: ErrRTCPHeader, Offset: f.FrameOffset, Stage: StageParse,
				Err: errors.New("RTCP block length out of bounds")}
		}
		if pType == typeSenderReport {
			// An SR is always 6 fixed words plus 6 words per reception block.
			minLen := 28 + rc*24
			if declared < minLen || len(buf) < minLen {
				return nil, &ParseError{Code: ErrRTCPHeader, Offset: f.FrameOffset, Stage: StageParse,
					Err: errors.New("sender report block too short")}
			}
			sr := SenderReport{
				SSRC:        binary.BigEndian.Uint32(buf[4:8]),
				NTPTime:     binary.BigEndian.Uint64(buf[8:16]),
				RTPTime:     binary.BigEndian.Uint32(buf[16:20]),
				PacketCount: binary.BigEndian.Uint32(buf[20:24]),
				OctetCount:  binary.BigEndian.Uint32(buf[24:28]),
				Offset:      f.FrameOffset,
			}
			out.Reports = append(out.Reports, sr)
		}
		buf = buf[declared:]
	}
	return out, nil
}
