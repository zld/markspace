package rtp

import (
	"bytes"
	"testing"

	"github.com/pion/rtp"
)

func mkPacket(ssrc uint32, seq uint16, ts uint32) Captured {
	return Captured{ArrivalNS: int64(seq) * 20_000_000, Packet: rtp.Packet{
		Header:  rtp.Header{Version: 2, PayloadType: 111, SSRC: ssrc, SequenceNumber: seq, Timestamp: ts},
		Payload: []byte{byte(seq), byte(seq >> 8), 0xaa},
	}}
}

func TestRepairReordersDeduplicatesAndWrapsSequence(t *testing.T) {
	r := NewRepairer(8)
	input := []Captured{
		mkPacket(42, 65534, 100),
		mkPacket(42, 65535, 120),
		mkPacket(42, 1, 160), // arrives before wrapped zero
		mkPacket(42, 0, 140),
		mkPacket(42, 0, 140), // duplicate
	}
	for _, c := range input {
		r.Add(c)
	}
	got := r.Flush()
	if got.Duplicates != 1 {
		t.Fatalf("duplicates=%d, want 1", got.Duplicates)
	}
	if got.Missing != 0 || len(got.Gaps) != 0 {
		t.Fatalf("expected no gaps, got missing=%d gaps=%+v", got.Missing, got.Gaps)
	}
	if len(got.Ordered) != 4 {
		t.Fatalf("ordered=%d, want 4 unique packets", len(got.Ordered))
	}
	wantExt := []uint64{65534, 65535, 65536, 65537}
	for i, want := range wantExt {
		if got.Ordered[i].Ext != want {
			t.Fatalf("ordered[%d].Ext=%d, want %d (all=%+v)", i, got.Ordered[i].Ext, want, extList(got.Ordered))
		}
	}
}

func TestRepairWindowGapAndTimeMapping(t *testing.T) {
	r := NewRepairer(4)
	for _, seq := range []uint16{100, 105, 106} {
		r.Add(mkPacket(7, seq, uint32(seq)*960))
	}
	got := r.Flush()
	if got.Duplicates != 0 {
		t.Fatalf("duplicates=%d", got.Duplicates)
	}
	if got.Missing != 4 || len(got.Gaps) != 1 {
		t.Fatalf("missing=%d gaps=%+v, want one 4-packet gap", got.Missing, got.Gaps)
	}
	g := got.Gaps[0]
	if g.StartExt != 101 || g.EndExt != 104 {
		t.Fatalf("gap ext=%d..%d, want 101..104", g.StartExt, g.EndExt)
	}
	gaps := AssignGapIntervals(got.Gaps, got.Ordered, 960, func(ts uint32) (int64, error) {
		// 48 kHz rational scale, expressed in nanoseconds.
		return int64(ts) * 1_000_000_000 / 48_000, nil
	})
	if !gaps[0].Mapped {
		t.Fatalf("gap not mapped: %s", gaps[0].MapError)
	}
	// Seq 100 TS=96000; first missing packet starts one 960-tick frame later.
	if want := int64(96960) * 1_000_000_000 / 48_000; gaps[0].StartNS != want {
		t.Fatalf("StartNS=%d want %d", gaps[0].StartNS, want)
	}
	if want := int64(105*960) * 1_000_000_000 / 48_000; gaps[0].EndNS != want {
		t.Fatalf("EndNS=%d want %d", gaps[0].EndNS, want)
	}
}

func TestTraceRoundTrip(t *testing.T) {
	records := []Captured{mkPacket(99, 10, 1000), mkPacket(99, 11, 1960)}
	var buf bytes.Buffer
	if err := WriteTrace(&buf, records); err != nil {
		t.Fatal(err)
	}
	got, err := ReadTrace(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1].Packet.SequenceNumber != 11 || got[0].Packet.SSRC != 99 {
		t.Fatalf("round trip mismatch: %+v", got)
	}
}

func extList(xs []Captured) []uint64 {
	out := make([]uint64, len(xs))
	for i := range xs {
		out[i] = xs[i].Ext
	}
	return out
}
