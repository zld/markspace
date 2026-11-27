package ingest

import (
	"encoding/binary"

	"github.com/pion/rtcp"

	"replay/internal/fault"
)

// ClockReport is an RTCP sender report reduced to the fields the replay
// kernel needs: the SSRC, the RTP timestamp, and the wallclock instant.
type ClockReport struct {
	Track       string
	SSRC        uint32
	RTPTime     uint32
	NTP         uint64
	WallNS      int64
	PacketCount uint32
	// Offset is the byte position of the report in the input stream.
	Offset int64
	// Index is the zero-based report order within the capture.
	Index int
}

// RTCPHeaderSize is the fixed size of an RTCP packet header.
const RTCPHeaderSize = 4

// ParseRTCP parses a single RTCP datagram that must contain exactly one
// sender report. It reports the byte offset of the first offending byte
// on truncation or malformed input.
//
// The capture format produced by the scenario generator stores one RTCP
// packet per record, so a compound packet is rejected: the replay kernel
// only consumes sender reports.
func ParseRTCP(track string, data []byte, offset int64, index int) (ClockReport, error) {
	base := fault.New(fault.KindTruncated, "rtcp packet too short").
		At("ingest").WithTrack(track).WithOffset(offset).WithIndex(index)

	if len(data) < RTCPHeaderSize {
		return ClockReport{}, base
	}
	version := data[0] >> 6
	if version != 2 {
		return ClockReport{}, fault.New(fault.KindInput, "unsupported rtcp version").
			At("ingest").WithTrack(track).WithOffset(offset).WithIndex(index)
	}
	count := int(data[0] & 0x1f)
	packetType := data[1]
	if packetType != 200 {
		return ClockReport{}, fault.New(fault.KindInput, "rtcp packet is not a sender report").
			At("ingest").WithTrack(track).WithOffset(offset).WithIndex(index)
	}
	// The RTCP length field counts 32-bit words minus one, including
	// the header. A sender report is 6 words plus 6 words per report
	// block.
	lengthWords := int(binary.BigEndian.Uint16(data[2:4])) + 1
	declared := lengthWords * 4
	if declared < 28 {
		return ClockReport{}, fault.New(fault.KindInput, "rtcp sender report declares implausible length").
			At("ingest").WithTrack(track).WithOffset(offset).WithIndex(index)
	}
	if len(data) < declared {
		return ClockReport{}, fault.New(fault.KindTruncated, "rtcp packet truncated").
			At("ingest").WithTrack(track).
			WithOffset(offset + int64(len(data))).
			WithIndex(index)
	}
	if want := 28 + 24*count; declared < want {
		return ClockReport{}, fault.New(fault.KindTruncated, "rtcp report block truncated").
			At("ingest").WithTrack(track).
			WithOffset(offset + int64(declared)).
			WithIndex(index)
	}

	ssrc := binary.BigEndian.Uint32(data[4:8])
	ntp := binary.BigEndian.Uint64(data[8:16])
	rtpTime := binary.BigEndian.Uint32(data[16:20])
	packetCount := binary.BigEndian.Uint32(data[20:24])

	return ClockReport{
		Track:       track,
		SSRC:        ssrc,
		RTPTime:     rtpTime,
		NTP:         ntp,
		PacketCount: packetCount,
		Offset:      offset,
		Index:       index,
	}, nil
}

// ParseRTCPWithPion cross-checks the manual parse with the pion/rtcp
// library and returns its diagnostic on failure. The manual parser stays
// authoritative for offsets, because pion reports only aggregate errors.
func ParseRTCPWithPion(track string, data []byte, offset int64, index int) (ClockReport, error) {
	report, err := ParseRTCP(track, data, offset, index)
	if err != nil {
		return ClockReport{}, err
	}
	packets, perr := rtcp.Unmarshal(data)
	if perr != nil {
		return ClockReport{}, fault.New(fault.KindInput, "rtcp library rejected sender report").
			At("ingest").WithTrack(track).WithOffset(offset).WithIndex(index).Wrap(perr)
	}
	found := false
	for _, p := range packets {
		if sr, ok := p.(*rtcp.SenderReport); ok {
			if sr.SSRC != report.SSRC || sr.RTPTime != report.RTPTime || sr.NTPTime != report.NTP {
				return ClockReport{}, fault.New(fault.KindInput, "rtcp libraries disagree on sender report fields").
					At("ingest").WithTrack(track).WithOffset(offset).WithIndex(index)
			}
			found = true
		}
	}
	if !found {
		return ClockReport{}, fault.New(fault.KindInput, "rtcp datagram has no sender report").
			At("ingest").WithTrack(track).WithOffset(offset).WithIndex(index)
	}
	return report, nil
}
