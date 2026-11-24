package report

import (
	"strings"
	"testing"
)

func TestSegmentHashesAndMismatch(t *testing.T) {
	h := NewHasher()
	h.Add(SegmentHash{
		Segment: "seg0.m4s", MediaSeq: 0,
		InputHash: Sum256([]byte("same")), OutputHash: Sum256([]byte("same")),
		InputBytes: 4, OutputBytes: 4,
	})
	h.Add(SegmentHash{
		Segment: "seg1.m4s", MediaSeq: 1,
		InputHash: Sum256([]byte("aaa")), OutputHash: Sum256([]byte("bbb")),
	})
	h.Add(SegmentHash{
		Segment: "seg2.m4s", MediaSeq: 2, Damaged: true,
		InputHash: Sum256([]byte("xxx")), OutputHash: "",
	})

	if h.Len() != 3 {
		t.Fatalf("hash count = %d", h.Len())
	}
	mm := h.Mismatches()
	if len(mm) != 1 || mm[0].Segment != "seg1.m4s" {
		t.Fatalf("mismatch list wrong: %+v", mm)
	}
	if fp1, fp2 := h.Fingerprint(), h.Fingerprint(); fp1 != fp2 || len(fp1) != 64 {
		t.Fatalf("fingerprint unstable or wrong length: %q %q", fp1, fp2)
	}
	// Ordering must be by media sequence regardless of insertion order.
	segs := h.Segments()
	if segs[0].MediaSeq != 0 || segs[2].MediaSeq != 2 {
		t.Fatalf("hash ordering wrong: %+v", segs)
	}
}

func TestMetricsRender(t *testing.T) {
	m := Metrics{
		TotalSegments: 3, DamagedSegments: 1,
		RoundtripConsistency: 0.6667, KeyframeHitRate: 1.0,
		Fingerprint: strings.Repeat("a", 64), ExitCode: 2,
	}
	raw, err := m.Render()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "\"roundtrip_consistency\": 0.6667") {
		t.Fatalf("rendered metrics missing field: %s", raw)
	}
}

func TestSum256Stable(t *testing.T) {
	if got := Sum256(nil); got != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatalf("empty hash wrong: %s", got)
	}
}
