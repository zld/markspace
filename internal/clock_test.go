package medpipe

import (
	"errors"
	"testing"
)

func TestErrorModel(t *testing.T) {
	b := NewErrorBuilder("v.h264")
	rec := b.RecoverableAt(CodeTruncatedUnit, "trailing NAL incomplete", "re-mux the tail", 1234)
	if !rec.Recoverable() || rec.Offset != 1234 || rec.Track != "v.h264" {
		t.Fatalf("recoverable error fields wrong: %+v", rec)
	}
	fatal := b.Fatal(CodeTimestampRepair, "ambiguous", "fix source")
	if fatal.Recoverable() {
		t.Fatal("fatal error reported as recoverable")
	}
	if !errors.Is(fatal, &PipelineError{Kind: KindFatal}) {
		t.Fatal("errors.Is kind match failed")
	}
	if !errors.Is(rec, &PipelineError{Code: CodeTruncatedUnit}) {
		t.Fatal("errors.Is code match failed")
	}
	if HasFatal([]*PipelineError{rec, fatal}) != true || HasFatal([]*PipelineError{rec}) != false {
		t.Fatal("HasFatal classification wrong")
	}
	if got := fatal.Error(); got == "" || !contains(got, "v.h264") {
		t.Fatalf("error string missing track: %q", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// pushSeq pushes a contiguous sequence with the given RTP timestamps.
func pushSeq(t *testing.T, c *Clock, seq uint16, ts uint32) *SampleRecord {
	t.Helper()
	r, err := c.Push(seq, ts)
	if err != nil {
		t.Fatalf("unexpected error at seq %d: %v", seq, err)
	}
	return r
}

func TestClockMonotonicClean(t *testing.T) {
	// 90 kHz video clock, 30 fps => 3000 ticks per frame.
	c := NewClock("v", 90000, 3000)
	for i := 0; i < 10; i++ {
		pushSeq(t, c, uint16(i), uint32(3600+i*3000))
	}
	stats, recs, _ := c.Finalize()
	if len(recs) != 10 {
		t.Fatalf("want 10 records, got %d", len(recs))
	}
	for _, r := range recs {
		if r.Anomaly != AnomalyNone || r.Correction != 0 {
			t.Fatalf("clean stream recorded anomaly: %+v", r)
		}
	}
	// 3000 ticks at 90 kHz = 33333.33... us per frame.
	if recs[1].UnifiedUS-recs[0].UnifiedUS != 33333 {
		t.Fatalf("unified delta = %d us", recs[1].UnifiedUS-recs[0].UnifiedUS)
	}
	if stats.MaxErrorUS != 0 || stats.MeanErrorUS != 0 {
		t.Fatalf("clean stream alignment error: %+v", stats)
	}
}

func TestClockRollbackAndDuplicate(t *testing.T) {
	c := NewClock("v", 90000, 3000)
	pushSeq(t, c, 0, 9000)
	pushSeq(t, c, 1, 12000)
	// Rollback: timestamp goes backwards.
	r, err := c.Push(2, 11000)
	if err != nil {
		t.Fatal(err)
	}
	if r.Anomaly != AnomalyRollback || r.RepairedTS != 15000 || r.Correction != 4000 {
		t.Fatalf("rollback repair wrong: %+v", r)
	}
	// Duplicate timestamp.
	r, err = c.Push(3, 11000)
	if err != nil {
		t.Fatal(err)
	}
	if r.Anomaly != AnomalyDuplicate || r.RepairedTS != 18000 {
		t.Fatalf("duplicate repair wrong: %+v", r)
	}
	stats, recs, _ := c.Finalize()
	if stats.Rollbacks != 1 || stats.Duplicates != 1 {
		t.Fatalf("stats wrong: %+v", stats)
	}
	for i := 1; i < len(recs); i++ {
		if recs[i].UnifiedUS <= recs[i-1].UnifiedUS {
			t.Fatal("repaired timestamps not strictly monotonic")
		}
	}
}

func TestClockMissing(t *testing.T) {
	c := NewClock("v", 90000, 3000)
	pushSeq(t, c, 0, 0)
	pushSeq(t, c, 1, 3000)
	// Two packets lost; timestamp jumps 3 deltas over one sequence step.
	r, err := c.Push(4, 12000)
	if err != nil {
		t.Fatal(err)
	}
	if r.Anomaly != AnomalyMissing {
		t.Fatalf("want missing anomaly, got %s", r.Anomaly)
	}
	stats, _, _ := c.Finalize()
	if stats.Missing != 2 {
		t.Fatalf("want 2 missing, got %d", stats.Missing)
	}
}

func TestClockAmbiguousGapFatal(t *testing.T) {
	c := NewClock("v", 90000, 3000)
	pushSeq(t, c, 0, 0)
	pushSeq(t, c, 1, 3000)
	// Gap of 4000 ticks is not an integral multiple of the 3000 delta.
	_, err := c.Push(3, 7000)
	if err == nil {
		t.Fatal("expected fatal error on ambiguous gap")
	}
	var pe *PipelineError
	if !errors.As(err, &pe) || pe.Kind != KindFatal || pe.Code != CodeTimestampRepair {
		t.Fatalf("wrong error: %v", err)
	}
}

func TestClockRTPWrap(t *testing.T) {
	c := NewClock("v", 90000, 3000)
	// Approach the 32-bit boundary, then wrap.
	base := uint32(0xFFFFFFFF) - 2*3000
	for i := 0; i < 6; i++ {
		pushSeq(t, c, uint16(i), base+uint32(i*3000))
	}
	stats, recs, _ := c.Finalize()
	if stats.Rollbacks != 0 {
		t.Fatalf("wrap misclassified as rollback: %+v", stats)
	}
	for i := 1; i < len(recs); i++ {
		if recs[i].RepairedTS <= recs[i-1].RepairedTS {
			t.Fatalf("wrap not monotonic at %d", i)
		}
	}
	// The wrapped sample keeps its original 32-bit value but a monotonic
	// repaired axis and a positive correction.
	if recs[3].OriginalTS != 0x00000BB7 { // base + 3*3000 truncated at 2^32
		t.Fatalf("wrapped original ts = %#x", recs[3].OriginalTS)
	}
	// After unwrapping, the repaired axis must be continuous across the
	// wrap boundary (delta exactly one nominal step).
	if recs[3].RepairedTS-recs[2].RepairedTS != 3000 {
		t.Fatalf("wrap boundary delta = %d", recs[3].RepairedTS-recs[2].RepairedTS)
	}
}

func TestClockAudio48k(t *testing.T) {
	// 20 ms audio frames at 48 kHz => 960 ticks.
	c := NewClock("a", 48000, 960)
	for i := 0; i < 5; i++ {
		pushSeq(t, c, uint16(i), uint32(i*960))
	}
	_, recs, _ := c.Finalize()
	if recs[1].UnifiedUS-recs[0].UnifiedUS != 20000 {
		t.Fatalf("audio frame duration = %d us, want 20000", recs[1].UnifiedUS-recs[0].UnifiedUS)
	}
}
