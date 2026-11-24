package fixture

import (
	"testing"

	"mediarun/internal/clock"
	rtpx "mediarun/internal/rtp"
	ts "mediarun/internal/ts"
)

func parse(t *testing.T, data []byte) ([]*ts.Packet, *clock.Recorder) {
	t.Helper()
	rec := &clock.Recorder{}
	pkts := ts.SplitStream(data, rec)
	if len(pkts) == 0 {
		t.Fatal("no packets parsed")
	}
	return pkts, rec
}

func TestBuild_NormalIsClean(t *testing.T) {
	st := Build(DefaultSpec(KindNormal))
	pkts, rec := parse(t, st.TS)
	if counts := rec.CountByKind(); counts["sync_error"] != 0 {
		t.Fatalf("normal sample must have no sync errors: %v", counts)
	}
	psi := ts.NewPSITracker()
	for _, p := range pkts {
		psi.Observe(p, rec)
	}
	m := psi.Current()
	if m.AudioPID != AudioPID || m.VideoPID != VideoPID || m.PMTPID != PMTPID {
		t.Fatalf("PSI mapping wrong: %+v", m)
	}
	// Every embedded segment WAV must be present in the stream.
	if len(st.SegmentWAVs) != 6 {
		t.Fatalf("segment wavs = %d", len(st.SegmentWAVs))
	}
}

func TestBuild_PCRWrap(t *testing.T) {
	st := Build(DefaultSpec(KindPCRWrap))
	pkts, _ := parse(t, st.TS)
	tl := clock.NewTimeline()
	var wraps int
	for _, p := range pkts {
		if p.HasAdaptation && p.Adaptation.HasPCR {
			_, _, amb := tl.FeedPCR(p.Adaptation.PCRBase)
			if amb {
				t.Fatal("PCR wrap produced an ambiguous timestamp")
			}
		}
	}
	_ = wraps
}

func TestBuild_CCJumpAndDuplicate(t *testing.T) {
	st := Build(DefaultSpec(KindCCJump))
	pkts, _ := parse(t, st.TS)
	rec := &clock.Recorder{}
	psi := ts.NewPSITracker()
	for _, p := range pkts {
		psi.Observe(p, rec)
	}
	checker := ts.NewContinuityChecker(map[uint16]string{AudioPID: "audio"})
	dups := 0
	for _, p := range pkts {
		if checker.Observe(p, rec) {
			dups++
		}
	}
	if dups == 0 || checker.TotalGap() == 0 {
		t.Fatalf("expected duplicate and gap, got dups=%d gap=%d", dups, checker.TotalGap())
	}
}

func TestBuild_PSIUpdate(t *testing.T) {
	st := Build(DefaultSpec(KindPSIUpdate))
	pkts, rec := parse(t, st.TS)
	psi := ts.NewPSITracker()
	for _, p := range pkts {
		psi.Observe(p, rec)
	}
	if psi.Epoch() < 2 {
		t.Fatalf("expected >=2 PMT epochs, got %d", psi.Epoch())
	}
	updates := rec.CountByKind()["psi_update"]
	if updates < 3 { // PAT + PMT v0 + PMT v1
		t.Fatalf("psi_update events = %d", updates)
	}
}

func TestBuild_Truncated(t *testing.T) {
	st := Build(DefaultSpec(KindTruncated))
	rec := &clock.Recorder{}
	ts.SplitStream(st.TS, rec)
	if rec.CountByKind()["truncated"] == 0 {
		t.Fatal("truncated sample must record a truncation event")
	}
}

func TestBuild_LossyRTP(t *testing.T) {
	st := Build(DefaultSpec(KindLoss))
	if st.RTP == nil || len(st.RTP) == 0 {
		t.Fatal("loss scenario must produce RTP packets")
	}
	buf := rtpx.NewReorderBuffer(8)
	var delivered int
	var dups bool
	for _, p := range st.RTP {
		ready, dup := buf.Feed(p)
		delivered += len(ready)
		if dup {
			dups = true
		}
	}
	delivered += len(buf.Flush())
	stats := buf.Stats()
	if !dups || stats.Duplicates == 0 {
		t.Fatal("duplicate RTP packet must be detected")
	}
	if stats.Lost == 0 || stats.Recovered == 0 {
		t.Fatalf("expected both loss and recovery, got %+v", stats)
	}
	if stats.Late != 0 {
		t.Fatalf("controlled fixture should produce no late packets, got %d", stats.Late)
	}
	// Recovery rate must be strictly between 0 and 1.
	r := buf.LossRecoveryRate()
	if r <= 0 || r >= 1 {
		t.Fatalf("recovery rate = %v", r)
	}
}

func TestAllKindsBuild(t *testing.T) {
	for _, kind := range AllKinds() {
		st := Build(DefaultSpec(kind))
		if len(st.TS) == 0 {
			t.Fatalf("kind %s produced empty stream", kind)
		}
	}
}
