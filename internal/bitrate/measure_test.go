package bitrate

import (
	"math"
	"testing"
)

func TestMeasureExactRate(t *testing.T) {
	// 900 kbps over 1000 ms = 112500 bytes.
	r := Measure(900, 1000, 250, []Observation{{StartMs: 0, EndMs: 1000, Bytes: 112500}})
	if math.Abs(r.ActualKbps-900) > 1e-9 {
		t.Fatalf("actual=%f", r.ActualKbps)
	}
	if math.Abs(r.ErrorPct) > 1e-9 {
		t.Fatalf("error=%f", r.ErrorPct)
	}
	if len(r.Windows) != 4 {
		t.Fatalf("windows=%+v", r.Windows)
	}
	if !r.WithinTolerance(5) {
		t.Fatalf("not within tolerance: %s", r)
	}
}

func TestMeasurePartialCoverageIsNotOverstated(t *testing.T) {
	// Only the second half was recovered. Rate must be computed on the
	// recovered 500 ms, not on the full duration.
	r := Measure(900, 1000, 250, []Observation{{StartMs: 500, EndMs: 1000, Bytes: 56250}})
	if math.Abs(r.ActualKbps-900) > 1e-9 {
		t.Fatalf("actual=%f", r.ActualKbps)
	}
	if r.MeasuredMs != 500 {
		t.Fatalf("measured=%d", r.MeasuredMs)
	}
	// Incomplete windows are omitted rather than reported as zero.
	if len(r.Windows) != 2 {
		t.Fatalf("windows=%+v", r.Windows)
	}
}

func TestMeasureDeviationDetected(t *testing.T) {
	r := Measure(1000, 1000, 250, []Observation{{StartMs: 0, EndMs: 1000, Bytes: 100000}}) // 800 kbps
	if math.Abs(r.ErrorPct-(-20)) > 1e-6 {
		t.Fatalf("error=%f", r.ErrorPct)
	}
	if r.WithinTolerance(5) {
		t.Fatal("20% error accepted by 5% tolerance")
	}
}

func TestMeasureWindowSpread(t *testing.T) {
	r := Measure(1000, 1000, 500, []Observation{
		{StartMs: 0, EndMs: 500, Bytes: 125000},   // 2000 kbps
		{StartMs: 500, EndMs: 1000, Bytes: 0},     // ignored: zero bytes
		{StartMs: 500, EndMs: 1000, Bytes: 62500}, // 1000 kbps
	})
	if len(r.Windows) != 2 {
		t.Fatalf("windows=%+v", r.Windows)
	}
	if r.WindowMax != 2000 || r.WindowMin != 1000 {
		t.Fatalf("min=%f max=%f", r.WindowMin, r.WindowMax)
	}
}

func TestMeasureClipsAndSorts(t *testing.T) {
	obs := SortedObs([]Observation{
		{StartMs: 800, EndMs: 1200, Bytes: 1000},
		{StartMs: -100, EndMs: 100, Bytes: 500},
	})
	if obs[0].StartMs != -100 {
		t.Fatalf("sorted=%+v", obs)
	}
	r := Measure(100, 1000, 250, []Observation{
		{StartMs: -100, EndMs: 100, Bytes: 500},
		{StartMs: 800, EndMs: 1200, Bytes: 1000},
		{StartMs: 300, EndMs: 300, Bytes: 50},
	})
	// Clipped to [0,100) and [800,1000): 1000 ms not covered, so the rate is
	// measured over the 300 ms that was.
	if r.TotalBytes != 1500 {
		t.Fatalf("bytes=%d", r.TotalBytes)
	}
	if r.MeasuredMs != 300 {
		t.Fatalf("measured=%d", r.MeasuredMs)
	}
}

func TestMeasureDefaultsAndEdge(t *testing.T) {
	r := Measure(0, 1000, 0, nil)
	if r.WindowMs != 1000 || r.ActualKbps != 0 || r.ErrorPct != 0 {
		t.Fatalf("report=%+v", r)
	}
}
