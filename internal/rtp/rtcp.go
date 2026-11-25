package rtp

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// ErrMalformedRTCP means a compound RTCP datagram is too short, has a
// bad version/length header or ends inside a report block.
var ErrMalformedRTCP = errors.New("malformed RTCP compound packet")

// SenderReport is the subset of an RFC 3550 SR (PT 200) that the
// pipeline needs to anchor the media clock: the SSRC, the RTP timestamp
// named in the report and the NTP wall-clock instant.
type SenderReport struct {
	SSRC          uint32
	RTPTimestamp  uint32
	NTPSeconds    float64
	PacketCount   uint32
	OctetCount    uint32
}

// ParseCompoundRTCP walks one compound RTCP datagram and returns every
// sender report it contains. Other report types are skipped according
// to their length fields, and padding is honored. A malformed datagram
// yields ErrMalformedRTCP so callers can record a localized fault.
func ParseCompoundRTCP(buf []byte) ([]SenderReport, error) {
	if len(buf) < 4 {
		return nil, fmt.Errorf("%w: shorter than one header", ErrMalformedRTCP)
	}
	var reports []SenderReport
	off := 0
	compoundPadding := false
	for off < len(buf) {
		if len(buf)-off < 4 {
			return nil, fmt.Errorf("%w: truncated header at byte %d", ErrMalformedRTCP, off)
		}
		hdr := buf[off]
		if version := hdr >> 6; version != 2 {
			return nil, fmt.Errorf("%w: RTP version %d is not 2", ErrMalformedRTCP, version)
		}
		padding := hdr&0x20 != 0
		if padding {
			compoundPadding = true
		}
		count := hdr & 0x1F
		payloadType := buf[off+1]
		lengthWords := int(binary.BigEndian.Uint16(buf[off+2:]))
		total := (lengthWords + 1) * 4
		if total <= 0 || off+total > len(buf) {
			return nil, fmt.Errorf("%w: length %d overruns datagram", ErrMalformedRTCP, total)
		}
		block := buf[off : off+total]

		if payloadType == 200 { // SR
			// header(4) + SSRC(4) + NTP(8) + RTP(4) + pkt(4) + oct(4) = 28
			minSize := 28 + int(count)*24
			if len(block) < minSize {
				return nil, fmt.Errorf("%w: SR block too short", ErrMalformedRTCP)
			}
			ntpHigh := binary.BigEndian.Uint32(block[8:12])
			ntpLow := binary.BigEndian.Uint32(block[12:16])
			reports = append(reports, SenderReport{
				SSRC:         binary.BigEndian.Uint32(block[4:8]),
				NTPSeconds:   float64(ntpHigh) + float64(ntpLow)/float64(1<<32),
				RTPTimestamp: binary.BigEndian.Uint32(block[16:20]),
				PacketCount:  binary.BigEndian.Uint32(block[20:24]),
				OctetCount:   binary.BigEndian.Uint32(block[24:28]),
			})
		}
		off += total
	}
	if compoundPadding && len(buf) >= 1 {
		pad := int(buf[len(buf)-1])
		if pad <= 0 || pad > len(buf) {
			return nil, fmt.Errorf("%w: invalid padding length %d", ErrMalformedRTCP, pad)
		}
	}
	return reports, nil
}

// BuildSenderReport serializes one minimal, non-compound SR with no
// reception blocks. Test fixtures use it to synthesize RTCP traffic.
func BuildSenderReport(sr SenderReport) []byte {
	const size = 28
	buf := make([]byte, size)
	buf[0] = 0x80 // V=2, no padding, RC=0
	buf[1] = 200
	binary.BigEndian.PutUint16(buf[2:], (size/4)-1)
	binary.BigEndian.PutUint32(buf[4:], sr.SSRC)
	secs := uint64(sr.NTPSeconds)
	frac := uint32((sr.NTPSeconds - float64(secs)) * float64(1<<32))
	binary.BigEndian.PutUint32(buf[8:], uint32(secs))
	binary.BigEndian.PutUint32(buf[12:], frac)
	binary.BigEndian.PutUint32(buf[16:], sr.RTPTimestamp)
	binary.BigEndian.PutUint32(buf[20:], sr.PacketCount)
	binary.BigEndian.PutUint32(buf[24:], sr.OctetCount)
	return buf
}
