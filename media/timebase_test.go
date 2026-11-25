package media

import (
	"errors"
	"testing"
)

func TestRTPToSamples(t *testing.T) {
	tb := TimeBase{SampleRate: 48000, RTPClock: 48000}
	got, err := tb.RTPToSamples(960)
	if err != nil || got != 960 {
		t.Fatalf("same-rate conversion = (%d, %v), want (960, nil)", got, err)
	}

	tb2 := TimeBase{SampleRate: 8000, RTPClock: 48000}
	got, err = tb2.RTPToSamples(48000)
	if err != nil || got != 8000 {
		t.Fatalf("divide-by-six conversion = (%d, %v), want (8000, nil)", got, err)
	}

	if _, err = tb2.RTPToSamples(1); !errors.Is(err, ErrInexactConversion) {
		t.Fatalf("inexact tick = %v, want ErrInexactConversion", err)
	}

	noClock := TimeBase{SampleRate: 48000}
	if _, err = noClock.RTPToSamples(10); !errors.Is(err, ErrNoRTPClock) {
		t.Fatalf("missing clock = %v, want ErrNoRTPClock", err)
	}

	noRate := TimeBase{RTPClock: 48000}
	if _, err = noRate.RTPToSamples(10); !errors.Is(err, ErrZeroSampleRate) {
		t.Fatalf("missing sample rate = %v, want ErrZeroSampleRate", err)
	}
}

func TestSamplesRTPRoundTrip(t *testing.T) {
	tb := TimeBase{SampleRate: 16000, RTPClock: 8000}
	ticks, err := tb.SamplesToRTP(2000)
	if err != nil || ticks != 1000 {
		t.Fatalf("SamplesToRTP = (%d, %v), want (1000, nil)", ticks, err)
	}
	samples, err := tb.RTPToSamples(ticks)
	if err != nil || samples != 2000 {
		t.Fatalf("round trip = (%d, %v), want (2000, nil)", samples, err)
	}
}

func TestMillisConversions(t *testing.T) {
	tb := TimeBase{SampleRate: 48000}
	ms, err := tb.SamplesToMillis(48000)
	if err != nil || ms != 1000 {
		t.Fatalf("SamplesToMillis = (%d, %v), want (1000, nil)", ms, err)
	}
	s, err := tb.MillisToSamples(250)
	if err != nil || s != 12000 {
		t.Fatalf("MillisToSamples = (%d, %v), want (12000, nil)", s, err)
	}
	// 1 ms at 44.1 kHz is fractional; the budget must round up, never down.
	tb2 := TimeBase{SampleRate: 44100}
	if s, err = tb2.MillisToSamples(1); err != nil || s != 45 {
		t.Fatalf("rounded-up MillisToSamples = (%d, %v), want (45, nil)", s, err)
	}
}

func TestExtend16SequenceWrap(t *testing.T) {
	// Simulate 65534, 65535, 0, 1 with a clean forward wrap.
	extended := uint64(65534)
	extended = Extend16(extended, 65535)
	extended = Extend16(extended, 0)
	if extended != 65536 {
		t.Fatalf("after wrap to 0 extended = %d, want 65536", extended)
	}
	extended = Extend16(extended, 1)
	if extended != 65537 {
		t.Fatalf("after wrap to 1 extended = %d, want 65537", extended)
	}
}

func TestExtend16Reorder(t *testing.T) {
	// A short backward step must be reported below the previous value, not as
	// a forward jump of nearly 64k.
	extended := uint64(1000)
	if got := Extend16(extended, 998); got != 998 {
		t.Fatalf("reordered sequence extended = %d, want 998", got)
	}
}

func TestExtend32TimestampWrap(t *testing.T) {
	base := uint64(0xFFFFFFFF)
	got := Extend32(base, 0)
	if got != 0x100000000 {
		t.Fatalf("wrapped timestamp extended = %d, want 4294967296", got)
	}
	got = Extend32(got, 10)
	if got != 0x10000000A {
		t.Fatalf("post-wrap timestamp extended = %d, want 4294967306", got)
	}
}
