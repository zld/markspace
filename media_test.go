package mediakernel

import (
	"math"
	"testing"
)

func TestClockConvertAndMonotonic(t *testing.T) {
	reg := NewTrackRegistry()
	reg.GetOrCreate(1)
	c := NewClock(1_000_000, reg)
	ns, err := c.Stamp(1, 40, 0)
	if err != nil || ns != 40_000_000 {
		t.Fatalf("Stamp = %d, %v", ns, err)
	}
	if _, err := c.Stamp(1, 40, 10); err != nil {
		t.Fatalf("equal timestamp must be allowed: %v", err)
	}
	if _, err := c.Stamp(1, 39, 20); err == nil {
		t.Fatalf("regression not detected")
	} else if mkErr := err.(*Error); mkErr.Code != ErrTSRegression || mkErr.Offset != 20 {
		t.Fatalf("err = %v", err)
	}
}

func TestClockOverflow(t *testing.T) {
	c := NewClock(1_000_000, NewTrackRegistry())
	_, err := c.Stamp(1, math.MaxInt64/1_000_000+1, 7)
	mkErr, ok := err.(*Error)
	if !ok || mkErr.Code != ErrTSOverflow || mkErr.Offset != 7 {
		t.Fatalf("err = %v, want %s at 7", err, ErrTSOverflow)
	}
}

func TestSegmenterHalfOpen(t *testing.T) {
	s := NewSegmenter(200_000_000)
	if got := s.Index(199_999_999); got != 0 {
		t.Fatalf("Index(199999999) = %d", got)
	}
	if got := s.Index(200_000_000); got != 1 {
		t.Fatalf("Index(200000000) = %d: block must belong to exactly one segment", got)
	}
	if got := s.Index(-1); got != -1 {
		t.Fatalf("Index(-1) = %d", got)
	}
	start, end := s.Range(2)
	if start != 400_000_000 || end != 600_000_000 {
		t.Fatalf("Range(2) = [%d,%d)", start, end)
	}
}

func TestSilenceMergeAndBreak(t *testing.T) {
	cfg := SilenceConfig{Threshold: 500, MinDuration: 50_000_000, MaxGap: 0}
	d := NewSilenceDetector(cfg)
	// Six contiguous silent 20ms blocks merge into one 120ms run.
	for i := int64(0); i < 6; i++ {
		d.Add(1, i*20_000_000, 20_000_000, 100, i > 0)
	}
	d.Add(1, 120_000_000, 20_000_000, 9000, true) // loud: ends the run
	d.Flush()
	if len(d.Intervals) != 1 {
		t.Fatalf("intervals = %d, want 1", len(d.Intervals))
	}
	iv := d.Intervals[0]
	if iv.Start != 0 || iv.End != 120_000_000 || iv.Peak != 100 {
		t.Fatalf("interval = %+v", iv)
	}
}

func TestSilenceGapAndSequenceBreak(t *testing.T) {
	cfg := SilenceConfig{Threshold: 500, MinDuration: 50_000_000, MaxGap: 0}
	d := NewSilenceDetector(cfg)
	for i := int64(0); i < 3; i++ {
		d.Add(1, i*20_000_000, 20_000_000, 100, i > 0)
	}
	// 40ms gap exceeds MaxGap: the run breaks instead of fabricating silence.
	d.Add(1, 100_000_000, 20_000_000, 100, true)
	d.Flush()
	if len(d.Intervals) != 1 || d.Intervals[0].End != 60_000_000 {
		t.Fatalf("intervals = %+v", d.Intervals)
	}
	// A sequence interruption (contiguous=false) must also break the run.
	d2 := NewSilenceDetector(cfg)
	for i := int64(0); i < 6; i++ {
		d2.Add(1, i*20_000_000, 20_000_000, 100, false)
	}
	d2.Flush()
	if len(d2.Intervals) != 0 {
		t.Fatalf("interrupted sequence fabricated silence: %+v", d2.Intervals)
	}
}

func TestFingerprintDeterminismAndDedup(t *testing.T) {
	payload := []byte("normalized-content")
	a := NewFingerprinter(1_000_000_000)
	h1, dup1 := a.Add(1, 0, payload)
	b := NewFingerprinter(1_000_000_000)
	h2, _ := b.Add(1, 0, payload)
	if h1 != h2 {
		t.Fatalf("same content produced different fingerprints: %s vs %s", h1, h2)
	}
	if dup1 {
		t.Fatalf("first occurrence marked duplicate")
	}
	if _, dup := a.Add(1, 100_000_000, payload); !dup {
		t.Fatalf("repeated content in same window not deduplicated")
	}
	if a.Duplicates != 1 || len(a.FPS) != 1 {
		t.Fatalf("dedup state: dups=%d fps=%d", a.Duplicates, len(a.FPS))
	}
	if _, dup := a.Add(1, 200_000_000, []byte("other")); dup {
		t.Fatalf("distinct content marked duplicate")
	}
}
