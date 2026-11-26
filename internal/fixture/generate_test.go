package fixture

import (
	"bytes"
	"context"
	"os/exec"
	"testing"

	"rtpregress/internal/rtp"
	"rtpregress/internal/ts"
)

func hasFFmpeg(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not available")
	}
	return path
}

// parseTrace runs the real RTP reorder + TS demux pipeline over a trace and
// returns the demux stats under isolate policy.
func parseTrace(t *testing.T, raw []byte, policy ts.Policy) ts.Stats {
	t.Helper()
	recs, _, err := rtp.ReadAllWithTruncation(context.Background(), bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("RTP read: %v", err)
	}
	rb := rtp.NewReorderBuffer()
	var tsBytes []byte
	for _, rec := range recs {
		for _, ev := range rb.Push(rec) {
			if ev.Kind == rtp.EventPacket {
				tsBytes = append(tsBytes, ev.Record.Packet.Payload...)
			}
		}
	}
	for _, ev := range rb.Flush() {
		if ev.Kind == rtp.EventPacket {
			tsBytes = append(tsBytes, ev.Record.Packet.Payload...)
		}
	}
	d := ts.NewDemuxer(bytes.NewReader(tsBytes), policy)
	st, err := d.Run(context.Background(), func(ts.TimelineEvent) {})
	if err != nil {
		t.Fatalf("demux: %v", err)
	}
	t.Logf("rtp stats: %+v", rb.Stats())
	return st
}

func TestGenerateCleanTraceParses(t *testing.T) {
	ff := hasFFmpeg(t)
	raw, err := BuildTrace(context.Background(), ff, Cases()[CaseClean])
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 || !bytes.HasPrefix(raw, TraceMagic[:]) {
		t.Fatal("trace missing RTP1 magic")
	}
	st := parseTrace(t, raw, ts.PolicyStrict)
	if st.PESPackets == 0 {
		t.Fatalf("clean trace must yield PES packets: %+v", st)
	}
	if st.SyncErrors != 0 || st.CCJumps != 0 {
		t.Fatalf("clean trace must have no faults: %+v", st)
	}
	if st.PCRSeen == 0 {
		t.Fatal("clean trace should carry PCR packets")
	}
}

func TestGenerateWrapTrace(t *testing.T) {
	ff := hasFFmpeg(t)
	raw, err := BuildTrace(context.Background(), ff, Cases()[CaseWrap])
	if err != nil {
		t.Fatal(err)
	}
	st := parseTrace(t, raw, ts.PolicyStrict)
	if st.PCRWraps < 1 {
		t.Fatalf("wrap case must expand at least one PCR wrap: %+v", st)
	}
}

func TestGenerateReorderTraceReportsDuplicatesAndLoss(t *testing.T) {
	ff := hasFFmpeg(t)
	raw, err := BuildTrace(context.Background(), ff, Cases()[CaseReorder])
	if err != nil {
		t.Fatal(err)
	}
	// use the reorder buffer directly to inspect RTP-level counters
	recs, _, err := rtp.ReadAllWithTruncation(context.Background(), bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	rb := rtp.NewReorderBuffer()
	for _, rec := range recs {
		rb.Push(rec)
	}
	rb.Flush()
	st := rb.Stats()
	if st.Duplicates == 0 {
		t.Fatalf("reorder case must include a duplicate: %+v", st)
	}
	if st.Lost == 0 && st.Late == 0 {
		t.Fatalf("reorder case must include a loss or late packet: %+v", st)
	}
}

func TestGenerateCCPSITrace(t *testing.T) {
	ff := hasFFmpeg(t)
	raw, err := BuildTrace(context.Background(), ff, Cases()[CaseCCPSI])
	if err != nil {
		t.Fatal(err)
	}
	st := parseTrace(t, raw, ts.PolicyIsolate)
	if st.CCJumps == 0 {
		t.Fatalf("cc_psi case must include a CC jump: %+v", st)
	}
	if st.PSIEvents < 3 { // initial PAT+PMT plus a bumped retransmit
		t.Fatalf("cc_psi case must show a PSI version event: %+v", st)
	}
}

func TestDamagedTraceIsolatesAndKeepsParsing(t *testing.T) {
	ff := hasFFmpeg(t)
	raw, err := BuildTrace(context.Background(), ff, Cases()[CaseDamaged])
	if err != nil {
		t.Fatal(err)
	}
	st := parseTrace(t, raw, ts.PolicyIsolate)
	if st.Gaps == 0 {
		t.Fatalf("damaged trace must report gaps: %+v", st)
	}
	if st.PESPackets == 0 {
		t.Fatalf("damaged trace must still deliver PES packets: %+v", st)
	}
	// strict policy must reject the same input with an error
	recs, _, _ := rtp.ReadAllWithTruncation(context.Background(), bytes.NewReader(raw))
	rb := rtp.NewReorderBuffer()
	var tsBytes []byte
	for _, rec := range recs {
		for _, ev := range rb.Push(rec) {
			if ev.Kind == rtp.EventPacket {
				tsBytes = append(tsBytes, ev.Record.Packet.Payload...)
			}
		}
	}
	d := ts.NewDemuxer(bytes.NewReader(tsBytes), ts.PolicyStrict)
	if _, err := d.Run(context.Background(), func(ts.TimelineEvent) {}); err == nil {
		t.Fatal("strict policy must return an error on the damaged trace")
	}
}

func TestTruncatedTraceReportsTruncation(t *testing.T) {
	ff := hasFFmpeg(t)
	raw, err := BuildTrace(context.Background(), ff, Cases()[CaseTrunc])
	if err != nil {
		t.Fatal(err)
	}
	st := parseTrace(t, raw, ts.PolicyIsolate)
	if st.Truncations == 0 {
		t.Fatalf("truncated case must count a truncation: %+v", st)
	}
}

func TestOrderedCaseNamesComplete(t *testing.T) {
	names := OrderedCaseNames()
	want := map[string]bool{
		CaseClean: true, CaseWrap: true, CaseDrift: true, CaseReorder: true,
		CaseCCPSI: true, CaseTrunc: true, CaseDamaged: true, CaseCancel: true,
	}
	if len(names) != len(want) {
		t.Fatalf("case count = %d", len(names))
	}
	for _, n := range names {
		if !want[n] {
			t.Fatalf("unexpected case %q", n)
		}
	}
}
