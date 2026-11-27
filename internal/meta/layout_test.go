package meta

import (
	"testing"

	"mediakernel/internal/clock"
	"mediakernel/internal/wav"
)

func TestStandardLayoutsAndValidation(t *testing.T) {
	for _, ch := range []uint16{1, 2, 4, 6, 8} {
		l, err := StandardLayouts(ch, 48000)
		if err != nil {
			t.Fatal(err)
		}
		if err := l.ValidateLayout(); err != nil {
			t.Fatalf("layout %s invalid: %v", l.Name, err)
		}
	}
	if _, err := StandardLayouts(3, 48000); err == nil {
		t.Fatal("3 channels has no standard layout")
	}
	bad := Layout{Name: "x", Channels: 2, ChannelTags: []string{"L"}, SampleRate: 48000}
	if err := bad.ValidateLayout(); err == nil {
		t.Fatal("tag/channel mismatch must fail")
	}
	zero := Layout{Name: "z", Channels: 2, ChannelTags: []string{"L", "R"}}
	if err := zero.ValidateLayout(); err == nil {
		t.Fatal("zero rate must fail")
	}
}

func TestCheckWAVChannelConflict(t *testing.T) {
	l, _ := StandardLayouts(2, 48000)
	h := wav.Header{Channels: 1, SampleRate: 48000, BitsPerSample: 16}
	err := CheckWAV(l, h)
	ce, ok := clock.AsError(err)
	if !ok || ce.Category != clock.CatLayoutMismatch || !ce.HasOffset {
		t.Fatalf("want located layout mismatch, got %v", err)
	}
}

func TestCheckWAVRateConflict(t *testing.T) {
	l, _ := StandardLayouts(2, 48000)
	h := wav.Header{Channels: 2, SampleRate: 44100, BitsPerSample: 16}
	err := CheckWAV(l, h)
	ce, ok := clock.AsError(err)
	if !ok || ce.Category != clock.CatLayoutMismatch || !ce.HasOffset {
		t.Fatalf("want located rate mismatch, got %v", err)
	}
}

func TestCheckWAVHappyPathAndRTP(t *testing.T) {
	l, _ := StandardLayouts(2, 48000)
	h := wav.Header{Channels: 2, SampleRate: 48000, BitsPerSample: 16}
	if err := CheckWAV(l, h); err != nil {
		t.Fatalf("matching layout rejected: %v", err)
	}
	if err := CheckRTPRate(l, 7, 48000); err != nil {
		t.Fatalf("matching rtp rate rejected: %v", err)
	}
	if err := CheckRTPRate(l, 7, 8000); err == nil {
		t.Fatal("rtp rate mismatch must fail")
	}
}

func TestRejectRangePreservesOffset(t *testing.T) {
	l, _ := StandardLayouts(2, 48000)
	h := wav.Header{Channels: 6, SampleRate: 48000, BitsPerSample: 16}
	base := CheckWAV(l, h)
	err := RejectRange(base, 123)
	ce, ok := clock.AsError(err)
	if !ok || ce.Category != clock.CatLayoutMismatch || !ce.HasOffset {
		t.Fatalf("range rejection must keep offset: %v", err)
	}
}
