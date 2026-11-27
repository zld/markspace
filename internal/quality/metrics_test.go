package quality

import (
	"math"
	"testing"

	"rtx/internal/wav"
)

func block(samples ...int64) wav.Block {
	return wav.Block{Samples: samples}
}

func TestMeasureCleanSignal(t *testing.T) {
	blocks := []wav.Block{
		block(10000, -10000), block(20000, -20000), block(30000, -30000),
	}
	m := Measure(blocks, 16, 3)
	if m.Peak != 30000.0/32768.0 {
		t.Fatalf("peak %f", m.Peak)
	}
	if m.RMS <= 0 || m.RMS > 1 {
		t.Fatalf("rms %f", m.RMS)
	}
	if m.Coverage != 1 || m.Clipped != 0 {
		t.Fatalf("coverage %f clipped %f", m.Coverage, m.Clipped)
	}
	if m.Score <= 0.9 {
		t.Fatalf("clean signal should score high: %f", m.Score)
	}
}

func TestMeasureExcludesCorruptRanges(t *testing.T) {
	// Four expected samples, only two valid blocks retained: the
	// corrupt range lowers coverage instead of contributing samples.
	blocks := []wav.Block{block(5000), block(-5000)}
	m := Measure(blocks, 16, 4)
	if m.Coverage != 0.5 {
		t.Fatalf("coverage %f, want 0.5", m.Coverage)
	}
	if m.SampleCount != 2 {
		t.Fatalf("count %d", m.SampleCount)
	}
}

func TestMeasureClipping(t *testing.T) {
	blocks := []wav.Block{block(32767), block(-32767), block(32767), block(100)}
	m := Measure(blocks, 16, 4)
	if m.Clipped != 0.75 {
		t.Fatalf("clipped %f", m.Clipped)
	}
	if m.Score >= 0.5 {
		t.Fatalf("clipped signal should score low: %f", m.Score)
	}
}

func TestMeasureEmpty(t *testing.T) {
	m := Measure(nil, 16, 10)
	if m.SampleCount != 0 || m.Coverage != 0 || m.Score != 0 {
		t.Fatalf("empty input must zero the metrics: %+v", m)
	}
}

func TestMeasureRMSValue(t *testing.T) {
	// Constant half-scale signal: RMS equals the amplitude.
	half := int64(16384)
	blocks := []wav.Block{block(half), block(half), block(half)}
	m := Measure(blocks, 16, 3)
	if math.Abs(m.RMS-0.5) > 1e-9 {
		t.Fatalf("rms %f, want 0.5", m.RMS)
	}
}
