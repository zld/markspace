package ingest

import (
	"time"

	"github.com/pion/rtcp"

	"medpipe/internal/clock"
)

// RTCPParser parses compound RTCP packets and feeds clock reports into the
// shared timeline.
type RTCPParser struct {
	MaxPacketBytes int
}

func NewRTCPParser() *RTCPParser {
	return &RTCPParser{MaxPacketBytes: DefaultMaxPacketBytes}
}

func (p *RTCPParser) Parse(raw []byte, sourceOffset int64, timeline *clock.Timeline, arrival time.Time) Item {
	max := p.MaxPacketBytes
	if max <= 0 {
		max = DefaultMaxPacketBytes
	}
	loc := &clock.Location{ByteOffset: sourceOffset, PacketByteOffset: sourceOffset}
	item := Item{Kind: "rtcp", Offset: sourceOffset, WireBytes: len(raw)}

	if len(raw) == 0 {
		item.Err = clock.Errorf(clock.CodeInvalidPacket, loc, "empty RTCP packet")
		return item
	}
	if len(raw) > max {
		item.Err = clock.Errorf(clock.CodePacketTooLarge, loc,
			"RTCP packet length %d exceeds maximum %d", len(raw), max)
		return item
	}

	packets, err := rtcp.Unmarshal(raw)
	if err != nil {
		item.Err = clock.Wrap(clock.CodeInvalidPacket, loc, err, "RTCP unmarshal failed")
		return item
	}

	for _, pkt := range packets {
		sr, ok := pkt.(*rtcp.SenderReport)
		if !ok {
			continue
		}
		item.SSRC = sr.SSRC
		loc.SSRC = sr.SSRC
		report := clock.SenderReport{
			SSRC: sr.SSRC, NTPTime: ntpToTime(sr.NTPTime), RTPTime: sr.RTPTime,
			PacketCount: sr.PacketCount, OctetCount: sr.OctetCount, SourceOffset: sourceOffset,
		}
		item.Reports = append(item.Reports, report)
		if ce := timeline.ApplySenderReport(report, arrival); ce != nil {
			item.Err = ce
		}
	}
	return item
}

const ntpEpochOffset = 2208988800

func ntpToTime(v uint64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	secs := int64(v>>32) - ntpEpochOffset
	frac := v & 0xffffffff
	nanos := int64((frac * 1_000_000_000) >> 32)
	return time.Unix(secs, nanos).UTC()
}

// TimeToNTP is used by the deterministic sample generator and tests.
func TimeToNTP(t time.Time) uint64 {
	if t.IsZero() {
		return 0
	}
	unix := t.Unix()
	secs := uint64(unix + ntpEpochOffset)
	nanos := uint64(t.Nanosecond())
	frac := (nanos << 32) / 1_000_000_000
	return secs<<32 | frac
}
