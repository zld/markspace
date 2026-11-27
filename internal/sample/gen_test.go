package sample

import (
	"testing"
)

func TestDeterministicAcrossRuns(t *testing.T) {
	a := GenRTPStream(7, 50, true)
	b := GenRTPStream(7, 50, true)
	if string(a.Records) != string(b.Records) {
		t.Fatal("RTP stream not deterministic")
	}
	if a.Lost == 0 || a.Dups == 0 {
		t.Fatalf("lossy stream should have loss and dups: %+v", a)
	}
	if a.Unique+a.Lost != 50 {
		t.Fatalf("unique %d + lost %d != 50", a.Unique, a.Lost)
	}
	if a.Packets != a.Unique+a.Dups {
		t.Fatalf("packets %d != unique %d + dups %d", a.Packets, a.Unique, a.Dups)
	}
}

func TestCleanStream(t *testing.T) {
	s := GenRTPStream(7, 20, false)
	if s.Lost != 0 || s.Dups != 0 || s.Packets != 20 || s.Unique != 20 {
		t.Fatalf("clean stream = %+v", s)
	}
}

func TestMediaDeterministic(t *testing.T) {
	f1 := Frame(7, 3)
	f2 := Frame(7, 3)
	if string(f1) != string(f2) {
		t.Fatal("frame not deterministic")
	}
	st := ComputePixelStats(Frames(7))
	if st.Peak <= 0 || st.Mean <= 0 {
		t.Fatalf("stats = %+v", st)
	}
	peak, db := LoudnessPeak(AudioPCM(7))
	if peak <= 0 || db >= 0 {
		t.Fatalf("peak=%d db=%f", peak, db)
	}
}
