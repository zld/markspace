// Package rtp validates cross-node RTP sequence and timestamp continuity
// using github.com/pion/rtp's packet representation.
package rtp

import (
	"fmt"

	pionrtp "github.com/pion/rtp"

	"mediacheck/internal/report"
	"mediacheck/internal/timeline"
)

// MaxRecoverableGap is the bounded number of sequence numbers that may be
// materialized as missing records. Larger jumps are treated as failure rather
// than guessing packet loss versus a stream restart.
const MaxRecoverableGap = 16

// Stream is one SSRC packet sequence in observed/capture order.
type Stream struct {
	Track     int
	SSRC      uint32
	ClockRate uint32
	Packets   []pionrtp.Packet
}

// Missing is one uniquely identified absent sequence number.
type Missing struct {
	Track        int
	SSRC         uint32
	Sequence     uint16
	PrevSequence uint16
	NextSequence uint16
	InputOffset  int
	TimeRange    timeline.TimeRange
	ByteDelta    int64
}

// Result contains recovered missing records and fatal structured issues.
type Result struct {
	Track   int
	SSRC    uint32
	Missing []Missing
	Issues  []report.Record
}

// ParsePacket unmarshals one serialized RTP packet. packetIndex is used as a
// stable diagnostic location because datagram packets have no file offset.
func ParsePacket(raw []byte, packetIndex int) (pionrtp.Packet, *report.Error) {
	var p pionrtp.Packet
	if err := p.Unmarshal(raw); err != nil {
		return p, report.NewError(report.RTPMalformedPacket, "malformed_rtp_packet", "", packetIndex, packetIndex,
			"RTP packet %d cannot be decoded: %v", packetIndex, err)
	}
	return p, nil
}

// ValidateStream checks uint16 sequence continuity and bounded timestamp wrap.
// allowed is a relative nanosecond interval (typically the fragment range).
func ValidateStream(stream Stream, allowed timeline.TimeRange, maxJumpTicks int64) Result {
	res := Result{Track: stream.Track, SSRC: stream.SSRC}
	if len(stream.Packets) == 0 {
		return res
	}
	unwrapper, err := timeline.NewUnwrapper(32, maxJumpTicks)
	if err != nil {
		res.Issues = append(res.Issues, report.Record{
			Type: "issue", Track: stream.Track, ErrorCode: report.TimelineBadBase,
			Category: "bad_wrap_bound", Severity: report.SeverityError, Message: err.Error(),
		})
		return res
	}
	firstExtended, terr := unwrapper.Unwrap(uint64(stream.Packets[0].Timestamp))
	if terr != nil {
		res.Issues = append(res.Issues, timelineIssue(stream.Track, terr))
		return res
	}
	firstExt := firstExtended
	lastExt := firstExtended

	for i := 1; i < len(stream.Packets); i++ {
		prev := stream.Packets[i-1]
		cur := stream.Packets[i]
		ext, werr := unwrapper.Unwrap(uint64(cur.Timestamp))
		if werr != nil {
			res.Issues = append(res.Issues, timelineIssue(stream.Track, werr))
			return res
		}

		prevMediaNS, nerr := timeline.RTPDeltaNS(lastExt-firstExt, stream.ClockRate)
		if nerr != nil {
			res.Issues = append(res.Issues, timelineIssue(stream.Track, nerr))
			return res
		}
		curMediaNS, nerr := timeline.RTPDeltaNS(ext-firstExt, stream.ClockRate)
		if nerr != nil {
			res.Issues = append(res.Issues, timelineIssue(stream.Track, nerr))
			return res
		}

		seqDelta := (uint32(cur.SequenceNumber) - uint32(prev.SequenceNumber)) & 0xFFFF
		switch {
		case seqDelta == 0:
			// Exact duplicate does not create a missing sequence record.
			lastExt = ext
			continue
		case seqDelta == 1:
			lastExt = ext
			continue
		case seqDelta > uint32(MaxRecoverableGap)+1:
			res.Issues = append(res.Issues, report.Record{
				Type: "issue", Track: stream.Track, Severity: report.SeverityError,
				InputOffset: -1, FailureOffset: i, ErrorCode: report.RTPSequenceDiscontinuity,
				Category: "sequence_gap_too_large",
				Message: fmt.Sprintf("SSRC=0x%08X seq %d->%d implies %d missing packets, over bound %d",
					stream.SSRC, prev.SequenceNumber, cur.SequenceNumber, seqDelta-1, MaxRecoverableGap),
			})
			return res
		}

		missingCount := int(seqDelta - 1)
		gapRange := timeline.TimeRange{StartNS: prevMediaNS, EndNS: curMediaNS}
		if curMediaNS < prevMediaNS {
			res.Issues = append(res.Issues, report.Record{
				Type: "issue", Track: stream.Track, Severity: report.SeverityError,
				FailureOffset: i, ErrorCode: report.RTPTimelineOutOfBounds, Category: "timestamp_backwards",
				TimeStartNS: prevMediaNS, TimeEndNS: curMediaNS,
				Message: fmt.Sprintf("RTP timestamp moved backwards from %d to %d ns", prevMediaNS, curMediaNS),
			})
			return res
		}
		if curMediaNS < allowed.StartNS || curMediaNS > allowed.EndNS || gapRange.Duration() > allowed.Duration()+1 {
			res.Issues = append(res.Issues, report.Record{
				Type: "issue", Track: stream.Track, Severity: report.SeverityError,
				FailureOffset: i, ErrorCode: report.RTPTimelineOutOfBounds, Category: "gap_outside_fragment",
				TimeStartNS: gapRange.StartNS, TimeEndNS: gapRange.EndNS,
				Message: fmt.Sprintf("sequence gap time range %s is outside allowed %s", gapRange, allowed),
			})
			return res
		}

		for j := 1; j <= missingCount; j++ {
			missingSeq := prev.SequenceNumber + uint16(j) // uint16 wraps exactly as RTP sequence numbers do
			res.Missing = append(res.Missing, Missing{
				Track:        stream.Track,
				SSRC:         stream.SSRC,
				Sequence:     missingSeq,
				PrevSequence: prev.SequenceNumber,
				NextSequence: cur.SequenceNumber,
				TimeRange:    gapRange,
				ByteDelta:    int64(len(cur.Payload) - len(prev.Payload)),
			})
		}
		lastExt = ext
	}
	return res
}

func timelineIssue(track int, e *report.Error) report.Record {
	rec := e.Record
	rec.Type = "issue"
	rec.Track = track
	if rec.Severity == "" {
		rec.Severity = report.SeverityError
	}
	return rec
}
