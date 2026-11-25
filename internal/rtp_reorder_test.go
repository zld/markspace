package kernel

import (
	"testing"
	"time"

	"github.com/pion/rtp"
)

func mustRTP(t *testing.T, seq uint16, payload []byte) []byte {
	t.Helper()
	p := &rtp.Packet{
		Header:  rtp.Header{Version: 2, PayloadType: 33, SequenceNumber: seq, Timestamp: uint32(seq) * 9000},
		Payload: payload,
	}
	b, err := p.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestReordererSortsAndCountsDuplicates(t *testing.T) {
	r := NewReorderer(ReorderConfig{Window: 8, BandwidthBps: 1_000_000})
	seqs := []uint16{10, 12, 11, 10}
	var got []uint16
	for i, seq := range seqs {
		out, gaps, err := r.Ingest(mustRTP(t, seq, []byte{byte(seq)}), uint64(i))
		if err != nil {
			t.Fatal(err)
		}
		if len(gaps) != 0 {
			t.Fatalf("unexpected gaps: %#v", gaps)
		}
		for _, p := range out {
			got = append(got, p.SequenceNumber)
		}
	}
	out, gaps := r.FlushLosses(99)
	if len(gaps) != 0 {
		t.Fatalf("unexpected flush gaps: %#v", gaps)
	}
	for _, p := range out {
		got = append(got, p.SequenceNumber)
	}
	if len(got) != 3 || got[0] != 10 || got[1] != 11 || got[2] != 12 {
		t.Fatalf("ordered sequences=%v", got)
	}
	if r.Stats().Duplicates != 1 {
		t.Fatalf("duplicates=%d want 1", r.Stats().Duplicates)
	}
}

func TestReordererLateOutsideWindowDoesNotRewriteCommit(t *testing.T) {
	r := NewReorderer(ReorderConfig{Window: 1, BandwidthBps: 1_000_000})
	for _, seq := range []uint16{1, 2, 4} {
		if _, _, err := r.Ingest(mustRTP(t, seq, nil), uint64(seq)); err != nil {
			t.Fatal(err)
		}
	}
	released, gaps := r.FlushLosses(7)
	if len(gaps) != 1 || gaps[0].Kind != GapRTPSequence || gaps[0].MissingCount != 1 {
		t.Fatalf("loss gaps=%#v released=%d", gaps, len(released))
	}
	if len(released) != 1 || released[0].SequenceNumber != 4 {
		t.Fatalf("flush order wrong: %#v", released)
	}

	before := r.Stats().Released
	out, lateGaps, err := r.Ingest(mustRTP(t, 3, nil), 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 || r.Stats().Released != before {
		t.Fatal("late packet changed committed output")
	}
	if len(lateGaps) != 1 || lateGaps[0].Kind != GapRTPLate {
		t.Fatalf("late gaps=%#v", lateGaps)
	}
}

func TestReordererWaitShrinksWithBandwidth(t *testing.T) {
	low := NewReorderer(ReorderConfig{Window: 32, BandwidthBps: 200_000}).WaitDuration()
	high := NewReorderer(ReorderConfig{Window: 32, BandwidthBps: 8_000_000}).WaitDuration()
	if high >= low {
		t.Fatalf("wait did not adapt: low=%s high=%s", low, high)
	}
	if low > 500*time.Millisecond || high < 20*time.Millisecond {
		t.Fatalf("wait outside bounds: low=%s high=%s", low, high)
	}
}

func TestReordererRejectsOversizedPacket(t *testing.T) {
	r := NewReorderer(ReorderConfig{MaxBytes: 16})
	_, _, err := r.Ingest(make([]byte, 17), 1)
	if code := ExitCodeOf(err); code != 1 {
		t.Fatalf("exit code=%d want 1 err=%v", code, err)
	}
}
