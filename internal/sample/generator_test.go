package sample

import (
	"bytes"
	"errors"
	"testing"

	"mediad/internal/clock"
	"mediad/ts"
)

func countKind(errs []clock.RangeError, kind clock.Kind) int {
	n := 0
	for _, e := range errs {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

func TestNormalFixtureIsClean(t *testing.T) {
	data, n := Generate(KindNormal)
	if len(data)%ts.PacketSize != 0 {
		t.Fatalf("normal fixture must be packet-aligned, got %d bytes", len(data))
	}
	res, err := ts.Demux(bytes.NewReader(data), ts.ScanOptions{})
	if err != nil {
		t.Fatalf("normal fixture must not be fatal: %v", err)
	}
	if len(res.Errors) != 0 {
		t.Fatalf("normal fixture must have zero errors, got %+v", res.Errors)
	}
	if len(res.Events) != n {
		t.Fatalf("events = %d, want %d video packets", len(res.Events), n)
	}
	// Monotonic time advances one millisecond per packet.
	if res.Events[n-1].End != int64(n)*PacketTicks {
		t.Fatalf("final tick = %d, want %d", res.Events[n-1].End, int64(n)*PacketTicks)
	}
}

func TestWrapFixtureReportsPCRWrap(t *testing.T) {
	data, _ := Generate(KindWrap)
	res, err := ts.Demux(bytes.NewReader(data), ts.ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if countKind(res.Errors, clock.KindPCRWrap) == 0 {
		t.Fatalf("wrap fixture must produce a pcr_wrap range error: %+v", res.Errors)
	}
	// Despite the raw wrap, the expanded timeline never goes backwards.
	for i := 1; i < len(res.Events); i++ {
		if res.Events[i].Start < res.Events[i-1].Start {
			t.Fatal("monotonic timeline went backwards across a PCR wrap")
		}
	}
}

func TestJumpFixtureReportsContinuity(t *testing.T) {
	data, _ := Generate(KindJump)
	res, err := ts.Demux(bytes.NewReader(data), ts.ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if countKind(res.Errors, clock.KindContinuity) == 0 {
		t.Fatalf("jump fixture must produce a continuity error: %+v", res.Errors)
	}
	if res.Duplicates != 0 {
		t.Fatalf("jump fixture has no duplicates, got %d", res.Duplicates)
	}
}

func TestPSIUpdateRejectsCorruptAndAppliesNewVersion(t *testing.T) {
	data, _ := Generate(KindPSIUpdate)
	res, err := ts.Demux(bytes.NewReader(data), ts.ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if countKind(res.Errors, clock.KindCRC) == 0 {
		t.Fatalf("corrupt PSI section must yield a CRC error: %+v", res.Errors)
	}
	if res.Map.Version != 2 {
		t.Fatalf("effective PSI version = %d, want 2", res.Map.Version)
	}
	// The video PID stays mapped after the version transition.
	if len(res.Map.EffectivePIDs) != 1 || res.Map.EffectivePIDs[0] != VideoPID {
		t.Fatalf("mapping after PSI update wrong: %v", res.Map.EffectivePIDs)
	}
}

func TestTruncateFixtureIsFatalWithOffset(t *testing.T) {
	data, truncOffset := Truncate()
	if len(data)%ts.PacketSize == 0 {
		t.Fatal("truncated fixture must not be packet-aligned")
	}
	res, err := ts.Demux(bytes.NewReader(data), ts.ScanOptions{})
	if err == nil {
		t.Fatal("truncated fixture must be fatal")
	}
	var re clock.RangeError
	if !errors.As(err, &re) || re.Kind != clock.KindTruncated {
		t.Fatalf("want truncated RangeError, got %v", err)
	}
	if re.Start != truncOffset {
		t.Fatalf("truncation offset = %d, want %d", re.Start, truncOffset)
	}
	if len(res.Errors) == 0 {
		t.Fatal("diagnostics must be retained on truncation")
	}
}

func TestAllKindsGenerated(t *testing.T) {
	kinds := AllKinds()
	if len(kinds) != 4 {
		t.Fatalf("want four fixture kinds, got %v", kinds)
	}
	seen := map[Kind]bool{}
	for _, k := range kinds {
		data, n := Generate(k)
		if len(data) == 0 || n == 0 {
			t.Fatalf("fixture %q generated no data", k)
		}
		seen[k] = true
	}
	for _, want := range []Kind{KindNormal, KindWrap, KindJump, KindPSIUpdate} {
		if !seen[want] {
			t.Fatalf("missing fixture kind %q", want)
		}
	}
}
