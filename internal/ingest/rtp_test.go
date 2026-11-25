package ingest

import (
	"errors"
	"math"
	"testing"

	"github.com/pion/rtp"

	"github.com/zld/mediacore/internal/model"
	"github.com/zld/mediacore/internal/trace"
)

func TestRTPClockConvertsAndExtendsWraparound(t *testing.T) {
	clock, err := NewRTPClock(2, 90000, 0)
	if err != nil {
		t.Fatal(err)
	}
	first, err := clock.Observe(&rtp.Packet{Header: rtp.Header{
		SequenceNumber: 65535, Timestamp: math.MaxUint32 - 5,
	}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := clock.Observe(&rtp.Packet{Header: rtp.Header{
		SequenceNumber: 0, Timestamp: 4,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if second.ExtendedTimestamp <= first.ExtendedTimestamp {
		t.Fatalf("timestamp did not extend across wrap: %d -> %d", first.ExtendedTimestamp, second.ExtendedTimestamp)
	}
	if second.SequenceDelta != 1 {
		t.Fatalf("sequence delta = %d, want 1 across wrap", second.SequenceDelta)
	}
	wantNS, err := model.TicksToNS(second.ExtendedTimestamp, 90000)
	if err != nil {
		t.Fatal(err)
	}
	if second.TimestampNS != wantNS {
		t.Fatalf("ns = %d, want %d", second.TimestampNS, wantNS)
	}
}

func TestRTPClockReportsRegressionWithTrack(t *testing.T) {
	clock, _ := NewRTPClock(9, 48000, 0)
	if _, err := clock.Observe(&rtp.Packet{Header: rtp.Header{Timestamp: 1000, SequenceNumber: 1}}); err != nil {
		t.Fatal(err)
	}
	_, err := clock.Observe(&rtp.Packet{Header: rtp.Header{Timestamp: 900, SequenceNumber: 2}})
	var te *trace.Error
	if !errors.As(err, &te) || te.Code != trace.CodeTimestampRegression || te.Stage != trace.StageRTP {
		t.Fatalf("unexpected error: %v", err)
	}
	if !te.HasTrack || te.TrackID != 9 || !te.HasTime {
		t.Fatalf("missing RTP location: %+v", te)
	}
}

func TestParseRTPPacket(t *testing.T) {
	src := &rtp.Packet{
		Header:  rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: 42, Timestamp: 77, SSRC: 1},
		Payload: []byte("media"),
	}
	raw, err := src.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseRTPPacket(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.SequenceNumber != 42 || got.Timestamp != 77 || string(got.Payload) != "media" {
		t.Fatalf("parsed packet mismatch: %+v", got)
	}
}
