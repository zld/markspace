package ingest

import (
	"errors"
	"testing"

	"github.com/pion/rtp"

	"medpipe/internal/fault"
)

func marshalRTP(t *testing.T, seq uint16, ts uint32, ssrc uint32) []byte {
	t.Helper()
	p := &rtp.Packet{
		Header: rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: seq, Timestamp: ts, SSRC: ssrc},
		Payload: []byte{1, 2, 3},
	}
	b, err := p.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRTPTrackerSequenceAndTimestampChecks(t *testing.T) {
	tr := NewRTPTracker()
	pkts := [][]byte{
		marshalRTP(t, 10, 1000, 42),
		marshalRTP(t, 11, 1090, 42),
		marshalRTP(t, 11, 1090, 42), // duplicate
		marshalRTP(t, 13, 1270, 42), // sequence gap
		marshalRTP(t, 12, 1180, 42), // reordered and timestamp regression
	}
	wantSeq := []string{"ok", "continuous", "duplicate", "gap", "reordered"}
	wantTS := []string{"ok", "monotonic", "same", "monotonic", "regressed"}
	for i, pkt := range pkts {
		rep, err := tr.Inspect(pkt, int64(i*16), 5)
		if err != nil {
			t.Fatalf("packet %d: %v", i, err)
		}
		if rep.SequenceStatus != wantSeq[i] {
			t.Fatalf("packet %d seq=%s want %s", i, rep.SequenceStatus, wantSeq[i])
		}
		if rep.TimestampStatus != wantTS[i] {
			t.Fatalf("packet %d ts=%s want %s", i, rep.TimestampStatus, wantTS[i])
		}
	}
}

func TestRTPInvalidPacketHasOffset(t *testing.T) {
	_, err := NewRTPTracker().Inspect([]byte{0, 1, 2}, 128, 9)
	if err == nil {
		t.Fatal("invalid RTP packet accepted")
	}
	var fe *fault.Error
	if !errors.As(err, &fe) || fe.Offset != 128 || fe.TrackID != 9 || fe.Stage != fault.StageRTP {
		t.Fatalf("unexpected error: %#v", err)
	}
}
