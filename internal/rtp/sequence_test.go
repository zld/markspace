package rtp_test

import (
	"math"
	"testing"

	pionrtp "github.com/pion/rtp"

	"mediacheck/internal/report"
	rtpcheck "mediacheck/internal/rtp"
	"mediacheck/internal/timeline"
)

func TestValidateStreamExpandsTimestampWrap(t *testing.T) {
	stream := rtpcheck.Stream{
		Track: 1, SSRC: 1, ClockRate: 90_000,
		Packets: []pionrtp.Packet{
			{Header: pionrtp.Header{SequenceNumber: 10, Timestamp: math.MaxUint32 - 100}},
			{Header: pionrtp.Header{SequenceNumber: 11, Timestamp: 100}},
		},
	}
	res := rtpcheck.ValidateStream(stream, timeline.TimeRange{StartNS: 0, EndNS: 10_000_000}, 10_000)
	if len(res.Issues) != 0 {
		t.Fatalf("expected wrapped timestamp to expand uniquely: %+v", res.Issues)
	}
	if len(res.Missing) != 0 {
		t.Fatalf("unexpected missing records: %+v", res.Missing)
	}
}

func TestValidateStreamRecoversGapAcrossSequenceWrap(t *testing.T) {
	stream := rtpcheck.Stream{
		Track: 2, SSRC: 2, ClockRate: 48_000,
		Packets: []pionrtp.Packet{
			{Header: pionrtp.Header{SequenceNumber: 65534, Timestamp: 0}},
			{Header: pionrtp.Header{SequenceNumber: 1, Timestamp: 960}},
		},
	}
	res := rtpcheck.ValidateStream(stream, timeline.TimeRange{StartNS: 0, EndNS: 40_000_000}, 0)
	if len(res.Issues) != 0 {
		t.Fatalf("gap across uint16 wrap should recover: %+v", res.Issues)
	}
	if len(res.Missing) != 2 {
		t.Fatalf("missing=%d want sequences 65535 and 0", len(res.Missing))
	}
	if res.Missing[0].Sequence != 65535 || res.Missing[1].Sequence != 0 {
		t.Fatalf("unexpected wrapped missing sequences: %+v", res.Missing)
	}
}

func TestValidateStreamRejectsAmbiguousTimestampJump(t *testing.T) {
	stream := rtpcheck.Stream{
		Track: 1, SSRC: 3, ClockRate: 90_000,
		Packets: []pionrtp.Packet{
			{Header: pionrtp.Header{SequenceNumber: 1, Timestamp: 10}},
			{Header: pionrtp.Header{SequenceNumber: 2, Timestamp: 200}},
		},
	}
	res := rtpcheck.ValidateStream(stream, timeline.TimeRange{StartNS: 0, EndNS: 100_000_000}, 10)
	if len(res.Issues) != 1 || res.Issues[0].ErrorCode != report.TimelineWrapAmbiguous {
		t.Fatalf("expected one wrap ambiguity issue, got %+v", res.Issues)
	}
}
