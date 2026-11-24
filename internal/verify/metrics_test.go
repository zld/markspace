package verify

import (
	"strings"
	"testing"

	"mediarun/internal/index"
	"mediarun/internal/parse"
)

func TestBitrateAndDeviation(t *testing.T) {
	// 300000 bytes over 2 s (180000 ticks) = 1.2 Mbps.
	b := Bitrate(300000, 180000)
	if b != 1200000 {
		t.Fatalf("bitrate = %d", b)
	}
	if d := DeviationPct(1200000, 1200000); d != 0 {
		t.Fatalf("deviation = %v", d)
	}
	if d := DeviationPct(1260000, 1200000); d != 5 {
		t.Fatalf("deviation = %v", d)
	}
	if d := DeviationPct(0, 1200000); d != 100 {
		t.Fatalf("deviation = %v", d)
	}
}

func TestThresholdCheck(t *testing.T) {
	m := &Metrics{
		BitrateDeviationPct: 4.9,
		MaxAlignmentErrorMs: 19,
		RandomAccessQueries: 10,
		RandomAccessHits:    10,
	}
	if bad := m.Check(DefaultThresholds()); len(bad) != 0 {
		t.Fatalf("unexpected violations: %v", bad)
	}
	m.BitrateDeviationPct = 5.1
	m.MaxAlignmentErrorMs = 21
	m.RandomAccessHits = 9
	bad := m.Check(DefaultThresholds())
	if len(bad) != 3 {
		t.Fatalf("violations = %v", bad)
	}
	joined := strings.Join(bad, ";")
	for _, want := range []string{"bitrate", "alignment", "hit rate"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %q", want, joined)
		}
	}
}

func TestProbeRandomAccess(t *testing.T) {
	f := &parse.Fragment{Path: "a.m4s"}
	var rep []uint64
	for i := 0; i < 10; i++ {
		f.Samples = append(f.Samples, parse.Sample{
			Index: i, DecodeTime: uint64(i) * 3000, Duration: 3000, Size: 1, Sync: true,
		})
		rep = append(rep, uint64(i)*3000)
	}
	ix, err := index.Build([]index.Source{{Fragment: f, Repaired: rep, Timescale: 90000}})
	if err != nil {
		t.Fatal(err)
	}
	q, h := ProbeRandomAccess(ix, 100)
	if q != 100 || h != 100 {
		t.Fatalf("probe = %d/%d", h, q)
	}
	m := &Metrics{RandomAccessQueries: q, RandomAccessHits: h}
	if m.HitRate() != 1.0 {
		t.Fatalf("hit rate = %v", m.HitRate())
	}
	if q, h := ProbeRandomAccess(nil, 10); q != 0 || h != 0 {
		t.Fatal("nil index must probe 0/0")
	}
}

func TestFillSummary(t *testing.T) {
	m := &Metrics{SampleCount: 300, Duration: 900000, RepairedTimestamps: 2}
	m.FillSummary()
	if m.QualitySummary != "samples=300 durationMs=10000 repairs=2" {
		t.Fatalf("summary = %q", m.QualitySummary)
	}
}
