package clock

import (
	"encoding/binary"
	"errors"
)

// ntpEpochOffsetSeconds is the difference between 1900-01-01 and 1970-01-01.
const ntpEpochOffsetSeconds uint64 = 2208988800

// RTCPReport contains the clock correspondence carried by an RTCP SR.
type RTCPReport struct {
	SSRC         uint32
	NTPNS        int64
	RTPTimestamp uint32
}

// NTPToUnixNS converts an NTP 64-bit timestamp to Unix nanoseconds.
func NTPToUnixNS(ntpSeconds, ntpFraction uint32) int64 {
	secs := int64(uint64(ntpSeconds) - ntpEpochOffsetSeconds)
	fracNS := (uint64(ntpFraction) * 1_000_000_000) >> 32
	return secs*1_000_000_000 + int64(fracNS)
}

// UnixNSToNTP is the inverse of NTPToUnixNS, used by deterministic fixtures.
func UnixNSToNTP(unixNS int64) (uint32, uint32) {
	unixSec := unixNS / 1_000_000_000
	remNS := unixNS % 1_000_000_000
	if remNS < 0 {
		unixSec--
		remNS += 1_000_000_000
	}
	ntpSec := uint64(unixSec) + ntpEpochOffsetSeconds
	frac := uint64(remNS) << 32 / 1_000_000_000
	return uint32(ntpSec), uint32(frac)
}

// ParseSenderReport parses only the clock fields needed by medcheck.
func ParseSenderReport(b []byte) (RTCPReport, error) {
	if len(b) < 28 {
		return RTCPReport{}, errors.New("rtcp: sender report is truncated")
	}
	version := b[0] >> 6
	packetType := b[1]
	if version != 2 {
		return RTCPReport{}, errors.New("rtcp: unsupported version")
	}
	if packetType != 200 {
		return RTCPReport{}, errors.New("rtcp: expected SR packet type 200")
	}
	wordLength := int(binary.BigEndian.Uint16(b[2:4]))
	if len(b) < (wordLength+1)*4 {
		return RTCPReport{}, errors.New("rtcp: declared length exceeds input")
	}
	ssrc := binary.BigEndian.Uint32(b[4:8])
	ntpMSW := binary.BigEndian.Uint32(b[8:12])
	ntpLSW := binary.BigEndian.Uint32(b[12:16])
	rtpTS := binary.BigEndian.Uint32(b[16:20])
	return RTCPReport{
		SSRC:         ssrc,
		NTPNS:        NTPToUnixNS(ntpMSW, ntpLSW),
		RTPTimestamp: rtpTS,
	}, nil
}

// MarshalSenderReport builds a minimal valid RTCP SR for tests and fixtures.
func MarshalSenderReport(r RTCPReport) []byte {
	b := make([]byte, 28)
	b[0] = 2 << 6
	b[1] = 200
	binary.BigEndian.PutUint16(b[2:4], 6) // six 32-bit words after header
	binary.BigEndian.PutUint32(b[4:8], r.SSRC)
	msw, lsw := UnixNSToNTP(r.NTPNS)
	binary.BigEndian.PutUint32(b[8:12], msw)
	binary.BigEndian.PutUint32(b[12:16], lsw)
	binary.BigEndian.PutUint32(b[16:20], r.RTPTimestamp)
	return b
}
