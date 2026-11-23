package ingest_test

import (
	"testing"

	"github.com/pion/rtp"

	"mediacore/internal/fault"
	"mediacore/internal/fixture"
	"mediacore/internal/ingest"
)

// buildRTPStream produces a clean stream and returns parsed fragments plus the
// raw bytes so RTP continuity can be analysed directly.
func buildRTPStream(t *testing.T, kinds ...fixture.FaultKind) ([]ingest.Fragment, map[string][]byte) {
	t.Helper()
	injected, err := fixture.Inject(41, 2, 12, kinds)
	if err != nil {
		t.Fatal(err)
	}
	r, f := ingest.NewReader("s.fmp4", injected.Data, ingest.DefaultLimits())
	if f != nil {
		t.Fatal(f)
	}
	info, f := ingest.ParseInit(r)
	if f != nil {
		t.Fatal(f)
	}
	idx, _ := ingest.ScanIndex(r, info.MoovOffset+info.MoovSize)
	res := r.ScanFragmentsBounded(info.MoovOffset+info.MoovSize, idx)
	return res.Fragments, map[string][]byte{"s.fmp4": injected.Data}
}

func TestAnalyzeRTRAcceptsCleanStream(t *testing.T) {
	frags, data := buildRTPStream(t)
	rep := ingest.AnalyzeRTP(frags, data)
	if rep.Malformed != 0 || rep.SequenceGaps != 0 || rep.Duplicates != 0 || rep.Reordered != 0 {
		t.Fatalf("clean stream reported rtp defects: %+v", rep)
	}
	if len(rep.Packets) != 24 {
		t.Fatalf("packets=%d want 24", len(rep.Packets))
	}
	// Both tracks must be represented with distinct SSRCs.
	ssrcs := map[uint32]bool{}
	for _, p := range rep.Packets {
		ssrcs[p.SSRC] = true
	}
	if len(ssrcs) != 2 {
		t.Fatalf("expected two SSRCs, got %d", len(ssrcs))
	}
}

func TestAnalyzeRTPDetectsInjectedGap(t *testing.T) {
	frags, data := buildRTPStream(t, fixture.FaultRTPSequenceGap)
	rep := ingest.AnalyzeRTP(frags, data)
	if rep.SequenceGaps != 1 {
		t.Fatalf("sequence gaps=%d want 1", rep.SequenceGaps)
	}
	if len(rep.Errors) != 1 || rep.Errors[0].Code != fault.CodeSequenceGap {
		t.Fatalf("errors=%v", rep.Errors)
	}
	inj, _ := fixture.Inject(41, 2, 12, []fixture.FaultKind{fixture.FaultRTPSequenceGap})
	if rep.Errors[0].Offset != inj.Injections[0].ByteOffset {
		t.Fatalf("gap offset=%d want %d", rep.Errors[0].Offset, inj.Injections[0].ByteOffset)
	}
}

func TestAnalyzeRTPSequenceWraparound(t *testing.T) {
	// A 16-bit counter must wrap without being reported as a reordering.
	var payloads []byte
	for i := 0; i < 4; i++ {
		buf := make([]byte, 32)
		h := rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: uint16(65534 + i), SSRC: 7}
		if _, err := h.MarshalTo(buf); err != nil {
			t.Fatal(err)
		}
		payloads = append(payloads, buf...)
	}
	frag := ingest.Fragment{
		Index: 0, File: "wrap.fmp4", TrackID: 1, Sequence: 1, MdatOffset: 0, MdatSize: int64(len(payloads)) + 8,
	}
	for i := 0; i < 4; i++ {
		frag.Samples = append(frag.Samples, ingest.Sample{Index: i, Offset: int64(i * 32), Size: 32})
	}
	rep := ingest.AnalyzeRTP([]ingest.Fragment{frag}, map[string][]byte{"wrap.fmp4": payloads})
	if rep.SequenceGaps != 0 || rep.Reordered != 0 || rep.Duplicates != 0 {
		t.Fatalf("wraparound misreported: %+v", rep)
	}
}

func TestAnalyzeRTPRejectsNonRTPPayload(t *testing.T) {
	buf := make([]byte, 64)
	// Version bits zero => not RTP v2.
	frag := ingest.Fragment{Index: 0, File: "bad.fmp4", TrackID: 1, Sequence: 1,
		Samples: []ingest.Sample{{Index: 0, Offset: 0, Size: 32}}}
	rep := ingest.AnalyzeRTP([]ingest.Fragment{frag}, map[string][]byte{"bad.fmp4": buf})
	if rep.Malformed != 1 {
		t.Fatalf("malformed=%d want 1", rep.Malformed)
	}
	if len(rep.Errors) == 0 || rep.Errors[0].Code != fault.CodeBadPayload {
		t.Fatalf("errors=%v", rep.Errors)
	}
	if rep.Errors[0].Offset != 0 {
		t.Fatalf("malformed offset=%d", rep.Errors[0].Offset)
	}
}
