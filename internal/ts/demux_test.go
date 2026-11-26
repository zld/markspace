package ts

import (
	"testing"

	"rtpregress/internal/clock"
)

const (
	testProg     uint16 = 1
	testPMTPID   uint16 = 0x1000
	testAudioPID uint16 = 0x0100
	testPCRPID   uint16 = 0x0100
	aacType      uint8  = 0x0F
)

func psiSetup() [][]byte {
	return [][]byte{
		patPacket(testProg, testPMTPID, 0, 0),
		pmtPacket(testProg, testAudioPID, testPCRPID, aacType, 0, 0),
	}
}

func TestPSIVersionAndAudioPID(t *testing.T) {
	pkts := psiSetup()
	evs, st, err := collect(pkts, PolicyStrict)
	if err != nil {
		t.Fatalf("clean PSI must parse: %v", err)
	}
	var pat, pmt *PSIEvent
	for i := range evs {
		if evs[i].PSI != nil && evs[i].PSI.Kind == "PAT" {
			pat = evs[i].PSI
		}
		if evs[i].PSI != nil && evs[i].PSI.Kind == "PMT" {
			pmt = evs[i].PSI
		}
	}
	if pat == nil || pmt == nil {
		t.Fatalf("missing PAT/PMT events: %+v", evs)
	}
	if pmt.PCRPID != testPCRPID {
		t.Fatalf("PCR PID = 0x%x", pmt.PCRPID)
	}
	if got := pmt.AudioTypes[testAudioPID]; got != aacType {
		t.Fatalf("audio stream type = 0x%x", got)
	}
	if st.PSIEvents != 2 {
		t.Fatalf("PSI events = %d", st.PSIEvents)
	}
}

func TestPSIVersionBumpAndRepeatedSection(t *testing.T) {
	pkts := psiSetup()
	// identical rebroadcast must not create another event
	pkts = append(pkts, patPacket(testProg, testPMTPID, 0, 1))
	// version bump must create one
	pkts = append(pkts, pmtPacket(testProg, testAudioPID, testPCRPID, aacType, 1, 1))
	evs, _, err := collect(pkts, PolicyStrict)
	if err != nil {
		t.Fatal(err)
	}
	pmtCount := 0
	patCount := 0
	for _, e := range evs {
		if e.PSI != nil && e.PSI.Kind == "PMT" {
			pmtCount++
		}
		if e.PSI != nil && e.PSI.Kind == "PAT" {
			patCount++
		}
	}
	if patCount != 1 || pmtCount != 2 {
		t.Fatalf("pat=%d pmt=%d, want 1/2 (repeat suppressed, bump emitted)",
			patCount, pmtCount)
	}
}

func TestPESAssemblyAndPTS(t *testing.T) {
	// 300-byte ES spans two TS packets (payload capacity is 184 each).
	es := make([]byte, 300)
	for i := range es {
		es[i] = byte(i)
	}
	pkts := psiSetup()
	pkts = append(pkts, pesMulti(testAudioPID, 0, 90000, es)...) // PTS = 1 s
	evs, st, err := collect(pkts, PolicyStrict)
	if err != nil {
		t.Fatal(err)
	}
	var pes *PESPacket
	for i := range evs {
		if evs[i].PES != nil {
			pes = evs[i].PES
		}
	}
	if pes == nil {
		t.Fatal("no PES emitted")
	}
	if len(pes.Data) != len(es) {
		t.Fatalf("ES length = %d, want %d (stuffing must be clipped)",
			len(pes.Data), len(es))
	}
	for i := range es {
		if pes.Data[i] != es[i] {
			t.Fatalf("ES byte %d = %x, want %x", i, pes.Data[i], es[i])
		}
	}
	if pes.PTSNanos != clock.NanosPerSecond {
		t.Fatalf("PTS = %d ns, want %d", pes.PTSNanos, clock.NanosPerSecond)
	}
	if st.PESPackets != 1 {
		t.Fatalf("PES count = %d", st.PESPackets)
	}
}

func TestPTSMonotonicAcrossPackets(t *testing.T) {
	pkts := psiSetup()
	pkts = append(pkts,
		pesStart(testAudioPID, 0, 90000, []byte{1}),
		pesStart(testAudioPID, 1, 180000, []byte{2}),
		pesStart(testAudioPID, 2, 270000, []byte{3}),
	)
	evs, _, err := collect(pkts, PolicyStrict)
	if err != nil {
		t.Fatal(err)
	}
	var last int64 = -1
	n := 0
	for _, e := range evs {
		if e.PES != nil {
			if e.PES.PTSNanos <= last {
				t.Fatal("PTS not strictly increasing")
			}
			last, n = e.PES.PTSNanos, n+1
		}
	}
	if n != 3 {
		t.Fatalf("want 3 PES, got %d", n)
	}
}

func TestCCJumpIsolateAndStrict(t *testing.T) {
	// A complete PES (CC 0, flushed by the next PUSI) is followed by a packet
	// whose CC jumps 1 -> 5, then parsing resumes cleanly at CC 6. Isolate
	// mode keeps both well-formed PES and records exactly one CC gap.
	pkts := psiSetup()
	good1 := pesStart(testAudioPID, 0, 90000, []byte{1})   // CC 0, PUSI
	jumped := pesStart(testAudioPID, 5, 180000, []byte{2}) // CC 0 -> 5, PUSI
	good2 := pesStart(testAudioPID, 6, 270000, []byte{3})  // CC 6, PUSI
	pkts = append(pkts, good1, jumped, good2)
	if _, _, err := collect(pkts, PolicyStrict); !isCode(err, clock.CodeTSCCJump) {
		t.Fatalf("strict must return CC error, got %v", err)
	}
	// isolate records a gap and still delivers the good PES packets
	evs, st, err := collect(pkts, PolicyIsolate)
	if err != nil {
		t.Fatalf("isolate must not fail: %v", err)
	}
	if st.CCJumps != 1 || st.Gaps == 0 {
		t.Fatalf("stats wrong: %+v", st)
	}
	var gap *Gap
	var pesN int
	for _, e := range evs {
		if e.Gap != nil {
			gap = e.Gap
		}
		if e.PES != nil {
			pesN++
		}
	}
	if gap == nil || gap.PID != testAudioPID {
		t.Fatalf("gap must carry audio PID: %+v", gap)
	}
	if gap.Offset < 0 {
		t.Fatal("gap must carry a packet offset")
	}
	if pesN != 3 {
		t.Fatalf("isolate should keep all PUSI PES, got %d PES", pesN)
	}
}

func TestGapCarriesTimeRange(t *testing.T) {
	// a good PES establishes a known PTS, then a CC jump must report a gap
	// whose time range starts at that PTS rather than an empty zero range.
	pkts := psiSetup()
	pkts = append(pkts,
		pesStart(testAudioPID, 0, 90000, []byte{1}),
		tsPacket(testAudioPID, false, false, true, 5, []byte{0xAD}),
	)
	evs, _, err := collect(pkts, PolicyIsolate)
	if err != nil {
		t.Fatal(err)
	}
	var gap *Gap
	for i := range evs {
		if evs[i].Gap != nil {
			gap = evs[i].Gap
		}
	}
	if gap == nil {
		t.Fatal("expected a gap event")
	}
	if gap.FromNanos != clock.NanosPerSecond {
		t.Fatalf("gap from_nanos = %d, want %d", gap.FromNanos, clock.NanosPerSecond)
	}
	if gap.ToNanos < gap.FromNanos {
		t.Fatalf("gap time range inverted: %+v", gap)
	}
}

func TestSyncErrorIsolateContinues(t *testing.T) {
	pkts := psiSetup()
	bad := tsPacket(testAudioPID, true, false, true, 0, []byte{0x01})
	bad[0] = 0x00 // corrupt sync byte
	pkts = append(pkts, bad, pesStart(testAudioPID, 1, 90000, []byte{9}))
	evs, st, err := collect(pkts, PolicyIsolate)
	if err != nil {
		t.Fatalf("isolate must survive sync error: %v", err)
	}
	if st.SyncErrors != 1 {
		t.Fatalf("sync errors = %d", st.SyncErrors)
	}
	found := false
	for _, e := range evs {
		if e.Gap != nil && e.Gap.Code == clock.CodeTSSync {
			found = true
			if e.Gap.Offset < 0 {
				t.Fatal("sync gap must carry packet offset")
			}
		}
	}
	if !found {
		t.Fatal("no sync gap event")
	}
}

func TestStrictSyncErrorReturnsErrorWithOffset(t *testing.T) {
	pkts := psiSetup()
	bad := tsPacket(testAudioPID, true, false, true, 0, nil)
	bad[0] = 0x48
	pkts = append(pkts, bad)
	_, _, err := collect(pkts, PolicyStrict)
	e := clock.AsError(err)
	if e == nil || e.Code != clock.CodeTSSync || e.Offset < 0 {
		t.Fatalf("want sync error with offset, got %v", err)
	}
}

func TestTruncatedTail(t *testing.T) {
	pkts := psiSetup()
	var buf []byte
	for _, p := range pkts {
		buf = append(buf, p...)
	}
	buf = append(buf, make([]byte, 40)...) // partial packet at the tail
	d := NewDemuxer(bytesReader(buf), PolicyStrict)
	_, err := d.Run(backgroundCtx(), func(TimelineEvent) {})
	if !isCode(err, clock.CodeTSTruncated) {
		t.Fatalf("want truncation error in strict, got %v", err)
	}
	d2 := NewDemuxer(bytesReader(buf), PolicyIsolate)
	st, err := d2.Run(backgroundCtx(), func(TimelineEvent) {})
	if err != nil || st.Truncations != 1 {
		t.Fatalf("isolate truncation: err=%v stats=%+v", err, st)
	}
}
