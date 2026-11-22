package packet

import "testing"

func info(seq uint16) RTPInfo {
	return RTPInfo{Seq: seq, Timestamp: uint32(seq) * 3000, SSRC: 1, Payload: []byte{byte(seq)}}
}

// collect feeds seqs and returns all emitted events.
func collect(r *Reorderer, seqs ...uint16) []Event {
	var out []Event
	for _, s := range seqs {
		out = append(out, r.Push(info(s))...)
	}
	return out
}

func rtpSeqs(evts []Event) []int64 {
	var out []int64
	for _, e := range evts {
		if e.Kind == KindRTP {
			out = append(out, e.ExtSeq)
		}
	}
	return out
}

func TestReorderInOrder(t *testing.T) {
	r := NewReorderer(ReordererConfig{Window: 4})
	evts := collect(r, 10, 11, 12, 13)
	got := rtpSeqs(evts)
	if len(got) != 4 || got[0] != 10 || got[3] != 13 {
		t.Fatalf("want 10..13 in order, got %v", got)
	}
	if r.Dups != 0 || r.Reordered != 0 {
		t.Fatalf("unexpected dups=%d reordered=%d", r.Dups, r.Reordered)
	}
}

func TestReorderWithinWindow(t *testing.T) {
	r := NewReorderer(ReordererConfig{Window: 4})
	evts := collect(r, 10, 12, 11, 13)
	got := rtpSeqs(evts)
	if len(got) != 4 {
		t.Fatalf("want 4 packets, got %v", got)
	}
	for i, want := range []int64{10, 11, 12, 13} {
		if got[i] != want {
			t.Fatalf("position %d: want %d, got %v", i, want, got)
		}
	}
	if r.Reordered != 1 {
		t.Fatalf("want 1 reordered packet, got %d", r.Reordered)
	}
}

func TestReorderLossBeyondWindow(t *testing.T) {
	r := NewReorderer(ReordererConfig{Window: 2})
	evts := collect(r, 10, 11, 15, 16, 17)
	var first *Event
	var total int64
	for i := range evts {
		if evts[i].Kind == KindLoss {
			if first == nil {
				first = &evts[i]
			}
			total += evts[i].LossTo - evts[i].LossFrom + 1
		}
	}
	if first == nil {
		t.Fatal("want loss events for the gap 12..14")
	}
	if first.LossFrom != 12 {
		t.Fatalf("first loss starts at %d, want 12", first.LossFrom)
	}
	if total != 3 {
		t.Fatalf("total lost = %d, want 3 (seqs 12,13,14)", total)
	}
	if got := rtpSeqs(evts); len(got) != 5 {
		t.Fatalf("want 5 packets emitted, got %v", got)
	}
	if flush := r.Flush(); len(flush) != 0 {
		t.Fatalf("nothing left to flush, got %+v", flush)
	}
}

func TestReorderDuplicatesDropped(t *testing.T) {
	r := NewReorderer(ReordererConfig{Window: 4})
	evts := collect(r, 10, 11, 11, 10, 12)
	if r.Dups != 2 {
		t.Fatalf("want 2 duplicates, got %d", r.Dups)
	}
	if got := rtpSeqs(evts); len(got) != 3 {
		t.Fatalf("duplicate payload counted: emitted %v", got)
	}
}

func TestReorderWrap(t *testing.T) {
	r := NewReorderer(ReordererConfig{Window: 4})
	evts := collect(r, 65534, 65535, 0, 1)
	got := rtpSeqs(evts)
	want := []int64{65534, 65535, 65536, 65537}
	if len(got) != 4 {
		t.Fatalf("want 4 packets, got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("position %d: want %d, got %d", i, want[i], got[i])
		}
	}
	// A straggler from before the wrap lands in the previous cycle.
	evts = r.Push(info(65535))
	if len(evts) != 0 || r.Dups != 1 {
		t.Fatalf("pre-wrap straggler: want duplicate, got evts=%v dups=%d", evts, r.Dups)
	}
}

func TestReorderResurrection(t *testing.T) {
	r := NewReorderer(ReordererConfig{Window: 2})
	collect(r, 10, 11, 15) // declares 12 lost (15-12 > 2)
	evts := r.Push(info(12))
	if len(evts) != 1 || !evts[0].Resurrected || evts[0].ExtSeq != 12 {
		t.Fatalf("want resurrected seq 12, got %+v", evts)
	}
	if r.Dups != 0 {
		t.Fatalf("resurrected packet counted as duplicate")
	}
	// A second copy of 12 is a duplicate.
	if evts := r.Push(info(12)); len(evts) != 0 || r.Dups != 1 {
		t.Fatalf("second copy: want duplicate, got evts=%v dups=%d", evts, r.Dups)
	}
}
