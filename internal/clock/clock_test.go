package clock_test

import (
	"errors"
	"math"
	"testing"

	"replaycheck/internal/clock"
)

func TestConvertAndDriftThreshold(t *testing.T) {
	ns, err := clock.Convert(90000, 90000)
	if err != nil || ns != clock.NanosecondsPerSecond {
		t.Fatalf("Convert(90000/90000) = %d, %v", ns, err)
	}
	if _, err := clock.Convert(1, 0); err == nil {
		t.Fatal("zero timescale must fail")
	}
	if clock.DriftExceeded(2_000_000) {
		t.Fatal("exactly 2 ms must not be marked as >2 ms")
	}
	if !clock.DriftExceeded(2_000_001) {
		t.Fatal("drift over 2 ms must fail")
	}
}

func TestUnwrapUnique32BitWrapOnce(t *testing.T) {
	const rate = 90_000
	start := uint32(math.MaxUint32 - 6000)
	stamps := []clock.Stamp{
		{Raw: start, Sequence: 1, Offset: 12},
		{Raw: start + 3000, Sequence: 2, Offset: 32},
		{Raw: start + 6000, Sequence: 3, Offset: 52},
		{Raw: start + 9000, Sequence: 4, Offset: 72}, // modulo wraps to 2999
	}
	rep := clock.UnwrapRTP(stamps, rate, 0)
	if rep.WrapCount != 1 {
		t.Fatalf("WrapCount = %d, want 1; anomalies=%+v", rep.WrapCount, rep.Anomalies)
	}
	if !rep.Monotonic() {
		t.Fatalf("expanded stamps are not monotonic: %+v", rep.Samples)
	}
	if len(rep.Anomalies) != 0 {
		t.Fatalf("clear wrap must not be an anomaly: %+v", rep.Anomalies)
	}
	startNS, endNS, off, endOff, ok := rep.WrapInterval(1)
	if !ok || off != 52 || endOff != 72 || endNS <= startNS {
		t.Fatalf("wrap interval ok=%v ns=[%d,%d] offsets=[%d,%d]", ok, startNS, endNS, off, endOff)
	}
}

func TestUnwrapReportsDuplicate(t *testing.T) {
	rep := clock.UnwrapRTP([]clock.Stamp{
		{Raw: 100, Offset: 10}, {Raw: 100, Offset: 30},
	}, 90000, 1000)
	if len(rep.Anomalies) != 1 || rep.Anomalies[0].Kind != clock.CodeDuplicateTimestamp {
		t.Fatalf("anomalies = %+v, want duplicate", rep.Anomalies)
	}
	if rep.Anomalies[0].Offset != 10 || rep.Anomalies[0].EndOffset != 30 {
		t.Fatalf("duplicate offsets = %d..%d", rep.Anomalies[0].Offset, rep.Anomalies[0].EndOffset)
	}
}

func TestUnwrapReportsRegressionAndAmbiguousWrap(t *testing.T) {
	reg := clock.UnwrapRTP([]clock.Stamp{
		{Raw: 1000, Offset: 1}, {Raw: 900, Offset: 2},
	}, 90000, 1000)
	if len(reg.Anomalies) != 1 || reg.Anomalies[0].Kind != clock.CodeRegression {
		t.Fatalf("regression anomalies = %+v", reg.Anomalies)
	}

	amb := clock.UnwrapRTP([]clock.Stamp{
		{Raw: 200_000_000, Offset: 8}, {Raw: 100_000_000, Offset: 16},
	}, 90000, 1000)
	if len(amb.Anomalies) != 1 || amb.Anomalies[0].Kind != clock.CodeAmbiguousWrap {
		t.Fatalf("ambiguous anomalies = %+v", amb.Anomalies)
	}
}

func TestUniformErrorAs(t *testing.T) {
	err := clock.NewError(clock.CodeBadHeader, "bad", 7)
	var ce *clock.Error
	if !errors.As(err, &ce) || ce.Offset != 7 {
		t.Fatalf("errors.As failed for %T", err)
	}
	a := clock.AnomalyFromError(err, "failure")
	if a.Kind != clock.CodeBadHeader || a.Offset != 7 {
		t.Fatalf("anomaly = %+v", a)
	}
}
