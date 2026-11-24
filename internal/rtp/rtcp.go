package rtp

import (
	"mediasync/internal/clock"
)

// RTCP packet type constants relevant to this library (RFC 3550).
const (
	rtcpSR      byte = 200
	srHeaderLen      = 8
	srSenderInfoLen  = 20
	ntpEpochOffset   = 2208988800 // 1900-01-01 to 1970-01-01 in seconds
)

// SenderReport is the subset of an RTCP SR used for clock drift fitting.
type SenderReport struct {
	SSRC        uint32
	RTPTime     uint32 // RTP timestamp field of the SR
	NTPSeconds  uint32 // NTP seconds
	NTPFraction uint32 // NTP fractional seconds
	ArrivalNS   int64  // local receive time in unix nanoseconds
}

// WallNS converts the NTP timestamp to unix nanoseconds.
func (s SenderReport) WallNS() int64 {
	secs := int64(s.NTPSeconds) - ntpEpochOffset
	// NTP fraction is 32-bit; scale to nanoseconds with int64 intermediates.
	fracNS := (int64(s.NTPFraction) * 1_000_000_000) >> 32
	return secs*1_000_000_000 + fracNS
}

// ParseSenderReport parses one RTCP compound packet and extracts the first
// Sender Report. Lengths are validated against clock bounds; a truncated or
// non-SR packet yields a classified error.
func ParseSenderReport(data []byte, arrivalNS int64) (SenderReport, *clock.Error) {
	if len(data) == 0 || len(data) > clock.MaxPacketBytes {
		return SenderReport{}, clock.NewError(clock.ErrInvalidLength, "rtcp packet length out of bounds")
	}
	off := 0
	for off+srHeaderLen <= len(data) {
		if data[off]>>6 != 2 {
			return SenderReport{}, clock.NewError(clock.ErrInvalidLength, "rtcp version must be 2")
		}
		payloadType := data[off+1]
		rc := int(data[off] & 0x1f)
		lengthWords := int(uint16(data[off+2])<<8 | uint16(data[off+3]))
		totalLen := (lengthWords + 1) * 4
		if totalLen <= 0 || off+totalLen > len(data) {
			return SenderReport{}, clock.NewError(clock.ErrTruncated, "rtcp length field exceeds datagram")
		}
		if payloadType == rtcpSR {
			minLen := srHeaderLen + srSenderInfoLen
			if totalLen < minLen {
				return SenderReport{}, clock.NewError(clock.ErrTruncated, "sender report missing sender info")
			}
			base := off + srHeaderLen
			sr := SenderReport{
				SSRC:        be32(data[off+4 : off+8]),
				NTPSeconds:  be32(data[base : base+4]),
				NTPFraction: be32(data[base+4 : base+8]),
				RTPTime:     be32(data[base+8 : base+12]),
				ArrivalNS:   arrivalNS,
			}
			return sr, nil
		}
		off += totalLen
		_ = rc // report blocks are skipped
	}
	return SenderReport{}, clock.NewError(clock.ErrInvalidLength, "no sender report in rtcp compound packet")
}

// be32 reads a big-endian uint32 from exactly four bytes.
func be32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}
