package rtp

import (
	"errors"
	"testing"
)

func TestRecoverRetransmissionFillsGap(t *testing.T) {
	rec, err := NewRecovery(Policy{MaxAttempts: 2, WaitTicks: 900, Now: 0})
	if err != nil {
		t.Fatal(err)
	}
	g, err := rec.Register(SeqRange{Start: 100, End: 102, Count: 3}, 1000, 1300, 500)
	if err != nil {
		t.Fatal(err)
	}
	if g.State != GapPending || g.Deadline != 900 {
		t.Fatalf("gap %+v", g)
	}
	reqs := rec.Due()
	if len(reqs) != 1 || reqs[0].Range.Count != 3 || reqs[0].Attempt != 1 {
		t.Fatalf("requests %+v", reqs)
	}
	for _, seq := range []uint16{101, 102, 100} {
		ok, err := rec.Offer(Packet{Seq: seq})
		if err != nil {
			t.Fatalf("offer %d: %v", seq, err)
		}
		if !ok {
			t.Fatalf("offer %d did not fill", seq)
		}
	}
	if open := rec.Open(); len(open) != 0 {
		t.Fatalf("still open: %+v", open)
	}
	st := rec.Stats()
	if st.RequestedPacket != 3 || st.RecoveredPacket != 3 || st.UnrecoveredGaps != 0 {
		t.Fatalf("stats %+v", st)
	}
}

func TestRecoverRejectsDuplicateAndUnknownFragments(t *testing.T) {
	rec, _ := NewRecovery(Policy{MaxAttempts: 2, WaitTicks: 900, Now: 0})
	rec.Register(SeqRange{Start: 50, End: 51, Count: 2}, 0, 0, 0)
	rec.Offer(Packet{Seq: 50})
	if _, err := rec.Offer(Packet{Seq: 50}); err != nil {
		t.Fatalf("duplicate offer should be reported without error: %v", err)
	}
	if _, err := rec.Offer(Packet{Seq: 99}); !errors.Is(err, ErrUnknownSequence) {
		t.Fatalf("unknown sequence: %v", err)
	}
	st := rec.Stats()
	if st.DuplicateFrag != 1 || st.RejectedFrag != 1 {
		t.Fatalf("stats %+v", st)
	}
}

func TestRecoverExpiresIntoMissingState(t *testing.T) {
	rec, _ := NewRecovery(Policy{MaxAttempts: 2, WaitTicks: 100, Now: 0})
	rec.Register(SeqRange{Start: 300, End: 302, Count: 3}, 9000, 9300, 1234)
	rec.Offer(Packet{Seq: 301})
	rec.Advance(101)
	lost := rec.Expire()
	if len(lost) != 1 || lost[0].State != GapLost || lost[0].Range.Count != 3 {
		t.Fatalf("lost %+v", lost)
	}
	if again := rec.Expire(); len(again) != 0 {
		t.Fatalf("expire repeated: %+v", again)
	}
	missing := rec.MissingState()
	if len(missing) != 1 {
		t.Fatalf("missing %+v", missing)
	}
	m := missing[0]
	if m.Range.Count != 3 || m.Packets != 2 || m.FirstTS != 9000 || m.LastTS != 9300 || m.UnifiedStart != 1234 {
		t.Fatalf("missing range %+v", m)
	}
	st := rec.Stats()
	if st.UnrecoveredGaps != 1 || st.UnrecoveredPkts != 2 {
		t.Fatalf("stats %+v", st)
	}
}

func TestRecoverAttemptsAreBounded(t *testing.T) {
	rec, _ := NewRecovery(Policy{MaxAttempts: 2, WaitTicks: 1000, Now: 0})
	rec.Register(SeqRange{Start: 1, End: 1, Count: 1}, 0, 0, 0)
	for i := 0; i < 5; i++ {
		n := len(rec.Due())
		if i < 2 && n != 1 {
			t.Fatalf("attempt %d: due %d", i, n)
		}
		if i >= 2 && n != 0 {
			t.Fatalf("attempt %d exceeded policy: due %d", i, n)
		}
	}
	if st := rec.Stats(); st.Requests != 2 {
		t.Fatalf("stats %+v", st)
	}
}

func TestRecoverLimitsPolicyAndGapSize(t *testing.T) {
	for _, p := range []Policy{{MaxAttempts: 0, WaitTicks: 1}, {MaxAttempts: 1, WaitTicks: 0}, {MaxAttempts: MaxRecoveryAttempts + 1, WaitTicks: 1}} {
		if _, err := NewRecovery(p); !errors.Is(err, ErrInvalidPolicy) {
			t.Fatalf("policy %+v accepted", p)
		}
	}
	rec, _ := NewRecovery(Policy{MaxAttempts: 1, WaitTicks: 1})
	if _, err := rec.Register(SeqRange{Count: MaxGapPackets + 1}, 0, 0, 0); !errors.Is(err, ErrGapTooLarge) {
		t.Fatalf("large gap accepted: %v", err)
	}
	if _, err := rec.Register(SeqRange{Count: 0}, 0, 0, 0); !errors.Is(err, ErrGapTooLarge) {
		t.Fatalf("empty gap accepted")
	}
}

func TestRecoverWrappedGap(t *testing.T) {
	rec, _ := NewRecovery(Policy{MaxAttempts: 1, WaitTicks: 1000})
	g, err := rec.Register(SeqRange{Start: 0xFFFE, End: 0x0001, Count: 4, Wrapped: true}, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, seq := range []uint16{0xFFFE, 0xFFFF, 0x0000, 0x0001} {
		if ok, err := rec.Offer(Packet{Seq: seq}); err != nil || !ok {
			t.Fatalf("offer %#04x: ok=%v err=%v", seq, ok, err)
		}
	}
	if g.State != GapRecovered {
		t.Fatalf("state %v", g.State)
	}
}

func TestRecoverOverlappingRegisterRejected(t *testing.T) {
	rec, _ := NewRecovery(Policy{MaxAttempts: 1, WaitTicks: 10})
	rec.Register(SeqRange{Start: 10, End: 20, Count: 11}, 0, 0, 0)
	if _, err := rec.Register(SeqRange{Start: 19, End: 25, Count: 7}, 0, 0, 0); !errors.Is(err, ErrGapTooLarge) {
		t.Fatalf("overlap accepted: %v", err)
	}
}
