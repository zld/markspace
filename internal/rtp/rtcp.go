package rtp

import (
	"encoding/binary"

	"mediatool/internal/clock"
	"mediatool/internal/fault"
)

// RTCP packet type for a Sender Report (RFC 3550 §6.4.1).
const rtcpSenderReport = 200

// ParseSenderReport decodes a bare RTCP SR packet. Compound packets are
// accepted as long as the first sub-packet is an SR.
func ParseSenderReport(body []byte, offset int64) (*clock.SenderReport, error) {
	if len(body) < 4 {
		return nil, (&fault.Fault{Kind: fault.KindTruncated, Offset: offset, Length: len(body),
			Recovery: fault.RecoverResync, Severity: fault.SeverityDamaged}).
			WithDetail("RTCP packet shorter than 4 bytes")
	}
	version := body[0] >> 6
	if version != 2 {
		return nil, (&fault.Fault{Kind: fault.KindRTCPInvalid, Offset: offset,
			Recovery: fault.RecoverContinue, Severity: fault.SeverityDamaged}).
			WithDetail("RTCP version %d, want 2", version)
	}
	if body[1] != rtcpSenderReport {
		return nil, (&fault.Fault{Kind: fault.KindRTCPInvalid, Offset: offset,
			Recovery: fault.RecoverContinue, Severity: fault.SeverityDamaged}).
			WithDetail("RTCP packet type %d, want sender report %d", body[1], rtcpSenderReport)
	}
	lengthWords := int(binary.BigEndian.Uint16(body[2:4]))
	packetLen := (lengthWords + 1) * 4
	if packetLen < 28 {
		return nil, (&fault.Fault{Kind: fault.KindRTCPInvalid, Offset: offset,
			Recovery: fault.RecoverContinue, Severity: fault.SeverityDamaged}).
			WithDetail("sender report length %d too short", packetLen)
	}
	if packetLen > len(body) {
		return nil, (&fault.Fault{Kind: fault.KindTruncated, Offset: offset, Length: len(body),
			Recovery: fault.RecoverResync, Severity: fault.SeverityDamaged}).
			WithDetail("sender report claims %d bytes, captured %d", packetLen, len(body))
	}
	sr := &clock.SenderReport{
		SSRC:        binary.BigEndian.Uint32(body[4:8]),
		NTP:         clock.NTP(binary.BigEndian.Uint64(body[8:16])),
		RTP:         binary.BigEndian.Uint32(body[16:20]),
		PacketCount: binary.BigEndian.Uint32(body[20:24]),
		OctetCount:  binary.BigEndian.Uint32(body[24:28]),
		Offset:      offset,
	}
	return sr, nil
}

// AppendSenderReport encodes a sender report into dst, for fixture generation
// and for tests.
func AppendSenderReport(dst []byte, sr clock.SenderReport) []byte {
	body := make([]byte, 28)
	body[0] = 0x80 // version 2, no padding, no reception report count
	body[1] = rtcpSenderReport
	binary.BigEndian.PutUint16(body[2:4], uint16(28/4-1))
	binary.BigEndian.PutUint32(body[4:8], sr.SSRC)
	binary.BigEndian.PutUint64(body[8:16], uint64(sr.NTP))
	binary.BigEndian.PutUint32(body[16:20], sr.RTP)
	binary.BigEndian.PutUint32(body[20:24], sr.PacketCount)
	binary.BigEndian.PutUint32(body[24:28], sr.OctetCount)
	return append(dst, body...)
}
