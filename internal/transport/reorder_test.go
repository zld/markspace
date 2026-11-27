package transport

import (
	"testing"

	"github.com/pion/rtp"

	"mediapipeline/internal/fault"
)

func datagram(t *testing.T, ssrc uint32, seq uint16, ts uint32, payload []byte) []byte {
	t.Helper()
	p := &rtp.Packet{
		Header:  rtp.Header{Version: 2, SSRC: ssrc, SequenceNumber: seq, Timestamp: ts},
		Payload: payload,
	}
	b, err := p.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func mustReorder(t *testing.T) (*Reorder, *fault.Report) {
	t.Helper()
	var rep fault.Report
	r, err := NewReorder(&rep)
	if err != nil {
		t.Fatal(err)
	}
	return r, &rep
}

func TestDedupAndReorder(t *testing.T) {
	r, rep := mustReorder(t)
	// Arrive 3,1,2,2 again: duplicate must be dropped, output must be 1,2,3.
	for _, seq := range []uint16{3, 1, 2, 2} {
		if err := r.Push(datagram(t, 7, seq, 0, []byte{byte(seq)}), int(seq)); err != nil {
			t.Fatal(err)
		}
	}
	out := r.Drain(7)
	var got []uint64
	for _, p := range out {
		got = append(got, p.Extended)
		if p.Payload[0] != byte(p.Extended) {
			t.Fatalf("payload/seq mismatch at %d", p.Extended)
		}
	}
	if len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Fatalf("ordered output = %v, want [1 2 3]", got)
	}
	if rep.CountCode(fault.CodeDuplicatePacket) != 1 || r.Duplicates(7) != 1 {
		t.Fatalf("duplicate not reported: %v", rep.All())
	}
}

func TestGapFormation(t *testing.T) {
	r, rep := mustReorder(t)
	for _, seq := range []uint16{10, 11, 14, 15} {
		if err := r.Push(datagram(t, 1, seq, 0, nil), 0); err != nil {
			t.Fatal(err)
		}
	}
	out := r.Drain(1)
	if len(out) != 2 { // 10,11 contiguous; 12,13 missing
		t.Fatalf("drained %d, want 2", len(out))
	}
	out = append(out, r.Flush(1)...)
	if len(out) != 4 {
		t.Fatalf("flushed to %d, want 4", len(out))
	}
	gaps := r.Gaps(1)
	if len(gaps) != 1 || gaps[0].From != 12 || gaps[0].To != 14 || gaps[0].Count() != 2 {
		t.Fatalf("gaps = %+v", gaps)
	}
	if rep.CountCode(fault.CodeGap) != 1 {
		t.Fatalf("gap count = %d", rep.CountCode(fault.CodeGap))
	}
	if r.TotalMissing() != 2 {
		t.Fatalf("total missing = %d, want 2", r.TotalMissing())
	}
}

func TestSequenceWrapAround(t *testing.T) {
	r, _ := mustReorder(t)
	// Start just below the 16-bit wrap; after 65535 comes 0.
	seqs := []uint16{65533, 65534, 65535, 0, 1, 2}
	for _, seq := range seqs {
		if err := r.Push(datagram(t, 9, seq, 0, nil), 0); err != nil {
			t.Fatal(err)
		}
	}
	out := r.Drain(9)
	if len(out) != len(seqs) {
		t.Fatalf("drained %d, want %d", len(out), len(seqs))
	}
	want := []uint64{65533, 65534, 65535, 65536, 65537, 65538}
	for i, p := range out {
		if p.Extended != want[i] {
			t.Fatalf("ext[%d]=%d, want %d", i, p.Extended, want[i])
		}
	}
}

func TestOutOfWindowResyncsWithGap(t *testing.T) {
	r, rep := mustReorder(t)
	// Establish base at 100, then push a packet far beyond the reorder window.
	// It must not be silently absorbed: a continuous gap is recorded, the
	// resync point is reported, and the far packet is still delivered.
	if err := r.Push(datagram(t, 3, 100, 0, nil), 0); err != nil {
		t.Fatal(err)
	}
	far := uint16(100 + MaxWindow + 50)
	if err := r.Push(datagram(t, 3, far, 0, nil), 0); err != nil {
		t.Fatal(err)
	}
	if !rep.Has(fault.CodeWindowExceeded) {
		t.Fatalf("out-of-window resync not reported: %v", rep.All())
	}
	out := r.Flush(3)
	if len(out) != 2 || out[0].Extended != 100 || out[1].Extended != uint64(far) {
		t.Fatalf("resync output = %+v", out)
	}
	gaps := r.Gaps(3)
	if len(gaps) != 1 || gaps[0].Count() != MaxWindow+49 {
		t.Fatalf("gaps = %+v", gaps)
	}
}

func TestLongLossBoundsBuffer(t *testing.T) {
	r, _ := mustReorder(t)
	// 100 contiguous packets, then a jump of 5000 with only the far end
	// present.  The buffer must stay bounded and the missing run reported.
	for seq := uint16(0); seq < 100; seq++ {
		if err := r.Push(datagram(t, 5, seq, 0, nil), 0); err != nil {
			t.Fatal(err)
		}
	}
	r.Drain(5)
	if err := r.Push(datagram(t, 5, 5099, 0, nil), 0); err != nil {
		t.Fatal(err)
	}
	if missing := r.TotalMissing(); missing != 4999 {
		t.Fatalf("total missing = %d, want 4999", missing)
	}
	out := r.Flush(5)
	if len(out) != 1 || out[0].Extended != 5099 {
		t.Fatalf("far packet not delivered: %+v", out)
	}
}

func TestPerSSRCIsolation(t *testing.T) {
	r, _ := mustReorder(t)
	for _, seq := range []uint16{1, 2} {
		if err := r.Push(datagram(t, 11, seq, 0, nil), 0); err != nil {
			t.Fatal(err)
		}
	}
	for _, seq := range []uint16{50, 51} {
		if err := r.Push(datagram(t, 22, seq, 0, nil), 0); err != nil {
			t.Fatal(err)
		}
	}
	if len(r.SSRCs()) != 2 {
		t.Fatalf("ssrcs = %v", r.SSRCs())
	}
	if got := len(r.Drain(11)); got != 2 {
		t.Fatalf("ssrc 11 drained %d", got)
	}
	if got := len(r.Drain(22)); got != 2 {
		t.Fatalf("ssrc 22 drained %d", got)
	}
}

func TestNilReportRejected(t *testing.T) {
	if _, err := NewReorder(nil); err == nil {
		t.Fatal("expected error for nil report")
	}
}
