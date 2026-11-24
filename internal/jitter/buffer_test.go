package jitter

import (
	"testing"

	"medcheck/internal/clock"
)

func pkt(ssrc uint32, seq uint64, pts int64) TimedPacket {
	return TimedPacket{Stream: clock.StreamAudio, SSRC: ssrc, ExtSeq: seq, PTSNS: pts, DurationNS: 20_000_000}
}

func dataSeqs(events []Event) []uint64 {
	var out []uint64
	for _, e := range events {
		if e.Kind == EventData {
			out = append(out, e.Packet.ExtSeq)
		}
	}
	return out
}

func gapSeqs(events []Event) []uint64 {
	var out []uint64
	for _, e := range events {
		if e.Kind == EventGap {
			out = append(out, e.Seqs...)
		}
	}
	return out
}

func TestBufferReordersWithinWindowAndSuppressesDuplicates(t *testing.T) {
	b := New(Config{WindowPackets: 4})
	var got []Event
	for _, seq := range []uint64{0, 2, 1, 3} {
		evs, dup, err := b.Push(pkt(1, seq, int64(seq)*20_000_000))
		if err != nil || dup {
			t.Fatalf("seq=%d dup=%v err=%v", seq, dup, err)
		}
		got = append(got, evs...)
	}
	// Seq zero is immediately released; 1-3 release in order once contiguous.
	if seqs := dataSeqs(got); len(seqs) != 4 || seqs[0] != 0 || seqs[1] != 1 || seqs[2] != 2 || seqs[3] != 3 {
		t.Fatalf("ordered seqs=%v all=%s", seqs, got)
	}
	if _, dup, _ := b.Push(pkt(1, 2, 40_000_000)); !dup {
		t.Fatal("duplicate queued packet was not suppressed")
	}
}

func TestBufferEmitsGapAfterPacketWindow(t *testing.T) {
	b := New(Config{WindowPackets: 3})
	var all []Event
	for _, seq := range []uint64{10, 13, 14, 15} {
		evs, _, _ := b.Push(pkt(9, seq, int64(seq)*20_000_000))
		all = append(all, evs...)
	}
	if gaps := gapSeqs(all); len(gaps) != 2 || gaps[0] != 11 || gaps[1] != 12 {
		t.Fatalf("gaps=%v events=%s", gaps, all)
	}
	if seqs := dataSeqs(all); len(seqs) != 4 || seqs[0] != 10 || seqs[1] != 13 {
		t.Fatalf("data=%v", seqs)
	}
	if _, dup, _ := b.Push(pkt(9, 11, 0)); !dup {
		t.Fatal("gap seq below next should be treated as duplicate/late")
	}
}

func TestBufferTimeWindowAndFlushTail(t *testing.T) {
	b := New(Config{WindowPackets: 8, MaxWaitNS: 30_000_000})
	b.Push(pkt(1, 0, 0))
	b.Push(pkt(1, 2, 40_000_000)) // seq 1 missing
	if evs := b.Advance(20_000_000); len(gapSeqs(evs)) != 0 {
		t.Fatalf("gap expired too early: %s", evs)
	}
	evs := b.Advance(40_000_000)
	if gaps := gapSeqs(evs); len(gaps) != 1 || gaps[0] != 1 {
		t.Fatalf("time gaps=%v events=%s", gaps, evs)
	}

	b.Push(pkt(2, 10, 0))
	b.SetFinalSeq(2, 12)
	flushed := b.Flush()
	if gaps := gapSeqs(flushed); len(gaps) != 2 || gaps[0] != 11 || gaps[1] != 12 {
		t.Fatalf("tail gaps=%v", flushed)
	}
}
