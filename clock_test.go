package mediakernel

import (
	"errors"
	"testing"
	"time"
)

func TestClockConversionsRoundTrip(t *testing.T) {
	c := NewClock(DefaultClockRate)
	// Duration has nanosecond resolution, so exact duration round trips need
	// tick counts whose duration is a whole number of nanoseconds (tick*6 at
	// 48 kHz, since 1e9/48000 = 125/6 ns per tick).
	durationCases := []int64{0, 6, 480, 48000, 48006, 1234566}
	for _, ticks := range durationCases {
		d := c.TicksToDuration(ticks)
		back := c.DurationToTicks(d)
		if back != ticks {
			t.Fatalf("duration round trip ticks=%d got=%d", ticks, back)
		}
	}
	// The float-millisecond path keeps sub-tick precision for any count.
	for _, ticks := range []int64{0, 1, 47, 48000, 48001, 1234567} {
		ms := c.TicksToMillis(ticks)
		if backMs := c.MillisToTicks(ms); backMs != ticks {
			t.Fatalf("millis round trip ticks=%d got=%d (ms=%v)", ticks, backMs, ms)
		}
	}
}

func TestClockDefaultRate(t *testing.T) {
	c := NewClock(0)
	if c.effectiveRate() != DefaultClockRate {
		t.Fatalf("expected default rate %d", DefaultClockRate)
	}
	if got := c.TicksToDuration(48000); got != time.Second {
		t.Fatalf("48000 ticks = %v, want 1s", got)
	}
	if got := c.TicksToMillis(48); got != 1.0 {
		t.Fatalf("48 ticks = %v ms, want 1.0", got)
	}
}

func TestClockAlternativeRate(t *testing.T) {
	c := NewClock(8000)
	if got := c.TicksToDuration(8000); got != time.Second {
		t.Fatalf("8000 ticks @8k = %v, want 1s", got)
	}
}

func TestTimeRangeContains(t *testing.T) {
	r := TimeRange{StartTicks: 100, EndTicks: 200}
	if !r.Contains(100) || !r.Contains(199) {
		t.Fatal("half-open range should include start, exclude end")
	}
	if r.Contains(200) || r.Contains(99) {
		t.Fatal("values outside range must not be contained")
	}
	if r.Duration() <= 0 {
		t.Fatal("expected positive duration")
	}
	empty := TimeRange{StartTicks: 10, EndTicks: 5}
	if empty.Duration() != 0 {
		t.Fatal("inverted range duration must be zero")
	}
}

func TestTimelineAdvanceAndReserve(t *testing.T) {
	tl := NewTimeline(NewClock(DefaultClockRate))
	r1 := tl.Advance(960)
	if r1.StartTicks != 0 || r1.EndTicks != 960 || tl.Cursor() != 960 {
		t.Fatalf("unexpected first range %+v cursor=%d", r1, tl.Cursor())
	}
	r2 := tl.Advance(960)
	if r2.StartTicks != 960 || r2.EndTicks != 1920 {
		t.Fatalf("unexpected second range %+v", r2)
	}
	// A lost packet reserves the missing span but creates no audio content.
	gap := tl.Reserve(960)
	if gap.StartTicks != 1920 || gap.EndTicks != 2880 || tl.Cursor() != 2880 {
		t.Fatalf("unexpected gap range %+v cursor=%d", gap, tl.Cursor())
	}
	r3 := tl.Advance(480)
	if r3.StartTicks != 2880 || r3.EndTicks != 3360 {
		t.Fatalf("post-gap range must continue after reserved span: %+v", r3)
	}
	if tl.LastPacketSamples() != 480 {
		t.Fatalf("last samples = %d, want 480", tl.LastPacketSamples())
	}
}

func TestMediaErrorStructure(t *testing.T) {
	seq := uint16(42)
	base := errors.New("short read")
	err := NewError(PhaseParse, 128, "truncated packet").
		WithSeq(seq).
		WithTime(TimeRange{StartTicks: 10, EndTicks: 20}).
		WithCause(base)
	if err.Phase != PhaseParse || err.Offset != 128 || err.Reason != "truncated packet" {
		t.Fatalf("fields lost: %+v", err)
	}
	if err.Seq == nil || *err.Seq != 42 {
		t.Fatal("seq attachment failed")
	}
	if !errors.Is(err, base) {
		t.Fatal("Unwrap chain must expose cause")
	}
	got, ok := AsMediaError(err)
	if !ok || got != err {
		t.Fatal("AsMediaError failed")
	}
	if _, ok := AsMediaError(base); ok {
		t.Fatal("plain error must not be classified as MediaError")
	}
}

func TestClassifyExitCode(t *testing.T) {
	if ClassifyExitCode(nil) != ExitOK {
		t.Fatal("nil error -> exit 0")
	}
	specErr := NewError(PhaseSpec, 0, "bitrate out of spec")
	if ClassifyExitCode(specErr) != ExitBusinessFail {
		t.Fatal("spec failure -> exit 1")
	}
	parseErr := NewError(PhaseParse, 7, "bad header")
	if ClassifyExitCode(parseErr) != ExitRuntimeError {
		t.Fatal("runtime/parse failure -> exit 2")
	}
	wrapped := NewError(PhaseMigrate, 0, "interrupted").WithCause(specErr)
	if ClassifyExitCode(wrapped) != ExitRuntimeError {
		t.Fatal("outer phase determines exit code; migration -> 2")
	}
}
