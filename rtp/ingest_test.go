package rtp

import (
	"bytes"
	"strings"
	"testing"

	"github.com/pion/rtp"
)

func testEvent(seq uint16) Event {
	return Event{
		Source: "s1", Kind: "video", Track: "v0", Seq: seq,
		PTSNS: int64(seq) * 40_000_000, ArrivalNS: int64(seq) * 1_000_000,
		Digest: strings.Repeat("a", 64),
	}
}

func TestTrackerWrapGapRecoveryAndDuplicate(t *testing.T) {
	tr := NewTracker()
	order := []uint16{65534, 65535, 2, 0, 1, 3, 3}
	dispositions := make([]Disposition, 0, len(order))
	for _, seq := range order {
		obs, err := tr.Observe(testEvent(seq))
		if err != nil {
			t.Fatal(err)
		}
		dispositions = append(dispositions, obs.Disposition)
	}
	want := []Disposition{
		DispositionFirst,
		DispositionSequential,
		DispositionGap,        // missing wrapped 0,1
		DispositionRecovered,
		DispositionRecovered,
		DispositionSequential,
		DispositionDuplicate,
	}
	for i := range want {
		if dispositions[i] != want[i] {
			t.Fatalf("step %d seq=%d disposition=%s want %s (all=%v)", i, order[i], dispositions[i], want[i], dispositions)
		}
	}
	got := tr.Totals()
	if got.LossDetected != 2 || got.Recovered != 2 || got.Duplicates != 1 || got.Unrecovered != 0 {
		t.Fatalf("bad totals: %+v", got)
	}
}

func TestTrackerFourLossFourRecoveryTwoDuplicates(t *testing.T) {
	tr := NewTracker()
	// Jump over 101,102 and later over 105,106; all four are recovered.
	for _, seq := range []uint16{100, 103, 101, 102, 104, 107, 105, 106, 107, 100} {
		if _, err := tr.Observe(testEvent(seq)); err != nil {
			t.Fatal(err)
		}
	}
	got := tr.Totals()
	if got.LossDetected != 4 || got.Recovered != 4 || got.Duplicates != 2 {
		t.Fatalf("loss=%d recovered=%d dup=%d", got.LossDetected, got.Recovered, got.Duplicates)
	}
}

func TestDecodeJSONLAndLocationError(t *testing.T) {
	good := `{"source":"s","kind":"audio","track":"a","seq":7,"pts_ns":1,"arrival_ns":2,"digest":"` + strings.Repeat("b", 64) + `"}` + "\n"
	events, err := DecodeJSONL(strings.NewReader(good), "events.jsonl")
	if err != nil || len(events) != 1 {
		t.Fatalf("events=%v err=%v", events, err)
	}
	bad := good + "{not-json}\n"
	_, err = DecodeJSONL(strings.NewReader(bad), "events.jsonl")
	if err == nil || !strings.Contains(err.Error(), "events.jsonl") {
		t.Fatalf("expected sourced JSONL error, got %v", err)
	}
}

func TestParsePacketWithPion(t *testing.T) {
	src := &rtp.Packet{
		Header: rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: 4242, SSRC: 7},
		Payload: []byte("fragment"),
	}
	raw, err := src.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	p, err := ParsePacket(raw)
	if err != nil {
		t.Fatal(err)
	}
	if p.SequenceNumber != 4242 || !bytes.Equal(p.Payload, []byte("fragment")) {
		t.Fatalf("parsed packet mismatch: %+v", p)
	}
}
