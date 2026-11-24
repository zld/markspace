package mediacore

import (
	"testing"
)

func feedOrFail(t *testing.T, s *RTPStream, raw []byte) {
	t.Helper()
	if err := s.Feed(raw); err != nil {
		t.Fatalf("Feed: %v", err)
	}
}

func TestRTPFixtureRecoveryCountsAndPayloadIdentity(t *testing.T) {
	fix, err := GenerateRTPFaults(RTPFaultConfig{
		Seed: 17, Packets: 48, LossBurst: 3, Reorder: 4, Duplicate: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(fix.Dropped) != 3 {
		t.Fatalf("dropped = %d, want 3", len(fix.Dropped))
	}
	s, err := NewRTPStream(DefaultRTPConfig(1, "video", 90000))
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range fix.Wire {
		feedOrFail(t, s, raw)
	}
	st := s.Flush()
	pkts := s.Packets()

	if st.Duplicates != 2 {
		t.Fatalf("duplicates = %d, want 2", st.Duplicates)
	}
	if st.Missing != uint64(len(fix.Dropped)) || st.Missing != 3 {
		t.Fatalf("missing = %d, want 3", st.Missing)
	}
	if st.Unique+st.Missing != 48 {
		t.Fatalf("unique %d + missing %d must equal source packets 48", st.Unique, st.Missing)
	}
	if err := validateRecovered(pkts, fix.Payload); err != nil {
		t.Fatalf("recovery invented or altered data: %v", err)
	}
	// Missing ranges must be reported with track and terminal state; reordered
	// holes that filled in time carry state=recovered and are also recorded.
	gaps := s.Gaps()
	if len(gaps) == 0 {
		t.Fatal("expected coalesced gap ranges")
	}
	var missingRanges uint64
	for _, g := range gaps {
		if g.TrackID != 1 {
			t.Fatalf("gap not track-tagged: %+v", g)
		}
		switch g.State {
		case GapMissing:
			if g.Count() == 0 {
				t.Fatalf("empty missing range: %+v", g)
			}
			missingRanges += g.Count()
		case GapRecovered, GapPending:
			// valid recorded lifecycle states
		default:
			t.Fatalf("gap has invalid state %q: %+v", g.State, g)
		}
	}
	if missingRanges != st.Missing || missingRanges != 3 {
		t.Fatalf("coalesced missing range total=%d stats.Missing=%d, want 3", missingRanges, st.Missing)
	}
}

func TestRTPDuplicateCountedOnce(t *testing.T) {
	s, _ := NewRTPStream(RTPConfig{TrackID: 7, ClockRate: 90000})
	raw := marshalRTP(1, 96, 10, 900, []byte("payload"), false)
	feedOrFail(t, s, raw)
	feedOrFail(t, s, raw)
	st := s.Flush()
	if st.Duplicates != 1 || st.Unique != 1 {
		t.Fatalf("dup=%d unique=%d, want 1/1", st.Duplicates, st.Unique)
	}
	if len(s.Packets()) != 1 {
		t.Fatalf("packets = %d, want 1", len(s.Packets()))
	}
}

func TestRTPReorderEmitsSortedAndFillsGap(t *testing.T) {
	s, _ := NewRTPStream(RTPConfig{TrackID: 1, ClockRate: 90000, Window: 16})
	pkt := func(seq uint16) []byte {
		return marshalRTP(0x9, 96, seq, uint32(seq)*3000, []byte{byte(seq)}, false)
	}
	feedOrFail(t, s, pkt(0))
	feedOrFail(t, s, pkt(2)) // opens pending gap at 1
	feedOrFail(t, s, pkt(1)) // late but inside window: fills it
	st := s.Flush()
	if st.Reordered == 0 {
		t.Fatal("expected at least one reorder event")
	}
	if st.LateFilled != 1 || st.Missing != 0 {
		t.Fatalf("lateFilled=%d missing=%d, want 1/0", st.LateFilled, st.Missing)
	}
	pkts := s.Packets()
	if len(pkts) != 3 || pkts[0].ExtSeq != 0 || pkts[1].ExtSeq != 1 || pkts[2].ExtSeq != 2 {
		t.Fatalf("packets not sorted by ext seq: %+v", pkts)
	}
}

func TestRTPOutOfWindowHoleMarkedMissingAndLateDiscarded(t *testing.T) {
	s, _ := NewRTPStream(RTPConfig{TrackID: 1, ClockRate: 90000, Window: 2})
	pkt := func(seq uint16) []byte {
		return marshalRTP(1, 96, seq, uint32(seq), []byte{byte(seq)}, false)
	}
	feedOrFail(t, s, pkt(0))
	feedOrFail(t, s, pkt(5)) // holes 1..4; 1,2 fall outside window immediately
	feedOrFail(t, s, pkt(8)) // pushes 3,4 terminal; opens 6,7
	feedOrFail(t, s, pkt(1)) // too late: already terminal -> discarded
	st := s.Flush()          // 6,7 become terminal
	if st.Missing != 6 {
		t.Fatalf("missing = %d, want 6 (seq 1-4,6-7)", st.Missing)
	}
	if st.Discarded != 1 {
		t.Fatalf("discarded = %d, want 1", st.Discarded)
	}
	gaps := s.Gaps()
	var found14, found67 bool
	for _, g := range gaps {
		if g.FirstSeq == 1 && g.LastSeq == 4 && g.State == GapMissing {
			found14 = true
		}
		if g.FirstSeq == 6 && g.LastSeq == 7 && g.State == GapMissing {
			found67 = true
		}
	}
	if !found14 || !found67 {
		t.Fatalf("expected coalesced ranges [1,4] and [6,7], got %+v", gaps)
	}
}

func TestRTPFlushMarksPendingMissing(t *testing.T) {
	s, _ := NewRTPStream(RTPConfig{TrackID: 1, ClockRate: 90000, Window: 64})
	pkt := func(seq uint16) []byte { return marshalRTP(1, 96, seq, 0, []byte{byte(seq)}, false) }
	feedOrFail(t, s, pkt(0))
	feedOrFail(t, s, pkt(3)) // holes 1,2 stay pending inside a large window
	if st := s.Stats(); st.Missing != 0 {
		t.Fatalf("before flush missing=%d, want 0", st.Missing)
	}
	st := s.Flush()
	if st.Missing != 2 {
		t.Fatalf("flush missing=%d, want 2", st.Missing)
	}
}

func TestRTPSequenceWrapUnrolls(t *testing.T) {
	s, _ := NewRTPStream(RTPConfig{TrackID: 1, ClockRate: 90000, Window: 64})
	for _, seq := range []uint16{65534, 65535, 0, 1} {
		feedOrFail(t, s, marshalRTP(1, 96, seq, 0, []byte{byte(seq)}, false))
	}
	st := s.Flush()
	if st.Missing != 0 {
		t.Fatalf("wrap must not create gaps, missing=%d", st.Missing)
	}
	pkts := s.Packets()
	if len(pkts) != 4 {
		t.Fatalf("packets=%d want 4", len(pkts))
	}
	if pkts[2].ExtSeq != 65536 || pkts[3].ExtSeq != 65537 {
		t.Fatalf("extended seq not unrolled across wrap: %d %d %d %d",
			pkts[0].ExtSeq, pkts[1].ExtSeq, pkts[2].ExtSeq, pkts[3].ExtSeq)
	}
}

func TestRTPRejectsMalformedAndForeignSSRC(t *testing.T) {
	s, _ := NewRTPStream(RTPConfig{TrackID: 1, ClockRate: 90000})
	if err := s.Feed([]byte{0x80, 0x60}); !IsKind(err, KindTransport) {
		t.Fatalf("short packet kind=%q err=%v", KindOf(err), err)
	}
	s2, _ := NewRTPStream(RTPConfig{TrackID: 1, ClockRate: 90000, SSRC: 42})
	foreign := marshalRTP(7, 96, 1, 0, nil, false)
	if err := s2.Feed(foreign); !IsKind(err, KindTransport) {
		t.Fatalf("foreign SSRC kind=%q err=%v", KindOf(err), err)
	}
	if _, err := NewRTPStream(RTPConfig{TrackID: 0, ClockRate: 90000}); !IsKind(err, KindInvalidConfig) {
		t.Fatal("zero track id must be invalid_config")
	}
}

func TestPacingChangesRhythmNotMedia(t *testing.T) {
	fix, err := GenerateRTPFaults(RTPFaultConfig{Seed: 3, Packets: 24})
	if err != nil {
		t.Fatal(err)
	}
	p := DefaultPipeline()
	_, pkts, sum, err := p.RunRTPPipeline(fix)
	if err != nil {
		t.Fatal(err)
	}
	delivered, steps, err := ApplyPacing(pkts, nil)
	if err != nil || delivered != len(pkts) || steps != 0 {
		t.Fatalf("no-bandwidth pacing = (%d,%d,%v)", delivered, steps, err)
	}
	if _, _, err := ApplyPacing(pkts, []int{100, 0}); !IsKind(err, KindInvalidConfig) {
		t.Fatalf("zero bandwidth kind=%q", KindOf(err))
	}
	fast, err := PacingAirtimeMillis(pkts, 4000)
	if err != nil {
		t.Fatal(err)
	}
	slow, err := PacingAirtimeMillis(pkts, 900)
	if err != nil {
		t.Fatal(err)
	}
	if slow <= fast {
		t.Fatalf("slow airtime %dms must exceed fast %dms", slow, fast)
	}
	if _, err := PacingAirtimeMillis(pkts, 0); !IsKind(err, KindInvalidConfig) {
		t.Fatalf("airtime zero bw kind=%q", KindOf(err))
	}
	// Pacing calculations are read-only with respect to payload/timestamps.
	if after := hashRecovered(pkts); after != sum {
		t.Fatal("pacing changed the recovered media hash")
	}
}
