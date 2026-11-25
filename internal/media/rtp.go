package media

import (
	"fmt"

	"github.com/pion/rtp"

	"archivecheck/internal/clock"
	"archivecheck/internal/diagnostic"
)

// RTPTimestampOffset is the fixed-header byte offset of the 32-bit timestamp.
const RTPTimestampOffset = 4

// ParsedRTP is the validated RTP sample projection.
type ParsedRTP struct {
	Packet      *rtp.Packet
	TimeNS      clock.Nanoseconds
	ExpectedNS  clock.Nanoseconds
	TimeDeltaNS clock.Nanoseconds
}

// ParseRTP parses an RTP packet and requires its timestamp to equal the
// deterministic expected value. A mismatch is reported at the timestamp bytes
// rather than silently adjusted.
func ParseRTP(sample int, sourceOffset int64, packetBytes []byte, clockRate, expectedTimestamp uint32) (*ParsedRTP, error) {
	if clockRate == 0 {
		return nil, diagnostic.New(diagnostic.CodeInvalidArgument, diagnostic.PhaseRTP, sample, sourceOffset,
			"RTP clock rate must not be zero")
	}
	var packet rtp.Packet
	if err := packet.Unmarshal(packetBytes); err != nil {
		return nil, diagnostic.Wrap(diagnostic.CodeInvalidRTP, diagnostic.PhaseRTP, sample, sourceOffset, err,
			"invalid RTP packet")
	}

	tsOffset := sourceOffset + RTPTimestampOffset
	if packet.Timestamp != expectedTimestamp {
		delta := int64(uint32(int64(packet.Timestamp) - int64(expectedTimestamp)))
		return nil, diagnostic.New(diagnostic.CodeBadRTPTimestamp, diagnostic.PhaseRTP, sample, tsOffset,
			"RTP timestamp %d does not match expected %d (wrap-aware raw delta %d)",
			packet.Timestamp, expectedTimestamp, delta)
	}

	gotNS := TimestampToNanoseconds(packet.Timestamp, clockRate)
	wantNS := TimestampToNanoseconds(expectedTimestamp, clockRate)
	return &ParsedRTP{
		Packet:      &packet,
		TimeNS:      gotNS,
		ExpectedNS:  wantNS,
		TimeDeltaNS: gotNS - wantNS,
	}, nil
}

// TimestampToNanoseconds converts a RTP timestamp in the supplied clock rate.
func TimestampToNanoseconds(timestamp uint32, clockRate uint32) clock.Nanoseconds {
	if clockRate == 0 {
		return 0
	}
	return clock.Nanoseconds((uint64(timestamp) * uint64(clock.Second)) / uint64(clockRate))
}

// WrapAwareDelta returns the signed 32-bit timestamp distance from prev to cur.
func WrapAwareDelta(prev, cur uint32) int32 {
	return int32(cur - prev)
}

// SequenceKey renders stable RTP identity fields.
func (p *ParsedRTP) SequenceKey() string {
	if p == nil || p.Packet == nil {
		return "rtp:nil"
	}
	return fmt.Sprintf("ssrc=%d:seq=%d:pt=%d", p.Packet.SSRC, p.Packet.SequenceNumber, p.Packet.PayloadType)
}
