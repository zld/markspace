package metrics

import (
	"math"
	"testing"
)

func TestSequenceAnalyzerWrapAndLoss(t *testing.T) {
	a := NewSequenceAnalyzer()
	seqs := []uint16{65534, 65535, 0, 1, 2, 4, 5}
	for _, s := range seqs {
		a.Add(s)
	}
	r := a.Report()
	if r.Received != len(seqs) {
		t.Fatalf("received=%d want %d", r.Received, len(seqs))
	}
	if r.Lost != 1 {
		t.Fatalf("lost=%d want 1 (seq 3)", r.Lost)
	}
	if r.Duplicates != 0 || r.Reordered != 0 {
		t.Fatalf("unexpected anomalies: %+v", r)
	}
	if r.FirstSeq != 65534 || r.LastSeq != 5 {
		t.Fatalf("span=%d..%d", r.FirstSeq, r.LastSeq)
	}
}

func TestSequenceAnalyzerDuplicates(t *testing.T) {
	a := NewSequenceAnalyzer()
	for _, s := range []uint16{10, 11, 11, 12, 10} {
		a.Add(s)
	}
	r := a.Report()
	if r.Duplicates != 2 {
		t.Fatalf("duplicates=%d want 2", r.Duplicates)
	}
	if r.Lost != 0 {
		t.Fatalf("lost=%d want 0", r.Lost)
	}
	if r.Reordered != 0 {
		t.Fatalf("reordered=%d want 0 for pure repeats", r.Reordered)
	}
}

func TestSequenceAnalyzerReordering(t *testing.T) {
	a := NewSequenceAnalyzer()
	for _, s := range []uint16{100, 102, 101, 103} {
		a.Add(s)
	}
	r := a.Report()
	if r.Lost != 0 || r.Reordered != 1 || r.Duplicates != 0 {
		t.Fatalf("unexpected report %+v", r)
	}
	if r.Received != 4 {
		t.Fatalf("received=%d", r.Received)
	}
}

func TestTimestampAnalyzerHandlesWrap(t *testing.T) {
	a := NewTimestampAnalyzer()
	a.Add(math.MaxUint32 - 1)
	a.Add(math.MaxUint32)
	a.Add(0)
	a.Add(3600)
	r := a.Report()
	if r.WrapCount != 1 {
		t.Fatalf("wraps=%d want 1", r.WrapCount)
	}
	if r.Regressions != 0 {
		t.Fatalf("regressions=%d want 0", r.Regressions)
	}
	if r.TotalSamples != 3602 {
		t.Fatalf("total samples=%d want 3602", r.TotalSamples)
	}
}
