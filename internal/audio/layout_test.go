package audio

import (
	"errors"
	"testing"

	"mediatool/internal/fault"
)

func TestKnownLayouts(t *testing.T) {
	for _, name := range []string{"mono", "stereo", "5.1", "7.1", "quad", "STEREO"} {
		l, err := Lookup(name)
		if err != nil {
			t.Fatalf("Lookup(%q): %v", name, err)
		}
		if l.Len() == 0 {
			t.Fatalf("layout %q has no channels", name)
		}
	}
}

func TestUnknownLayoutIsLocatedFault(t *testing.T) {
	_, err := Lookup("atmos-9.1.6")
	if err == nil {
		t.Fatal("expected error for unknown layout")
	}
	f := UnknownLayoutFault("atmos-9.1.6", 3, 4096)
	if f.Kind != fault.KindLayout || f.TrackID != 3 || f.Offset != 4096 {
		t.Fatalf("bad fault %+v", f)
	}
	if f.Severity != fault.SeverityFatal {
		t.Fatalf("unknown layout must be fatal, got %v", f.Severity)
	}
}

func TestDefaultMapNeverSilentlyDropsAChannel(t *testing.T) {
	from, _ := Lookup("5.1")
	to, _ := Lookup("stereo")
	m, err := DefaultMap(from, to)
	if err != nil {
		t.Fatalf("DefaultMap: %v", err)
	}
	if len(m.Dropped) == 0 {
		t.Fatal("5.1 -> stereo should record dropped channels for audit")
	}
	// Every source channel is either mapped or explicitly dropped.
	accounted := make(map[int]bool)
	for _, src := range m.Index {
		if src >= 0 {
			accounted[src] = true
		}
	}
	for _, d := range m.Dropped {
		accounted[d] = true
	}
	for i := 0; i < from.Len(); i++ {
		if !accounted[i] {
			t.Fatalf("source channel %d (%s) accounted for by neither map nor drop list",
				i, from.Channels[i])
		}
	}
}

func TestValidateRejectsSilentDrop(t *testing.T) {
	from, _ := Lookup("5.1")
	to, _ := Lookup("stereo")
	// A map that produces two outputs but ignores three source channels.
	m := ChannelMap{From: from, To: to, Index: []int{0, 1}}
	err := m.Validate()
	if err == nil {
		t.Fatal("expected validation to reject a silent drop")
	}
}

func TestApplyProducesMappedOrder(t *testing.T) {
	stereo, _ := Lookup("stereo")
	quad, _ := Lookup("quad")
	m, err := DefaultMap(stereo, quad)
	if err != nil {
		t.Fatalf("DefaultMap: %v", err)
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("map invalid: %v", err)
	}
	// One frame of stereo: FL=1, FR=2.
	out, err := m.Apply([]float32{1, 2}, 1, 1, 0)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(out) != quad.Len() {
		t.Fatalf("output channels = %d, want %d", len(out), quad.Len())
	}
	if out[0] != 1 || out[1] != 2 {
		t.Fatalf("FL/FR not preserved: %v", out)
	}
	// BL/BR are not representable by stereo and must be silence.
	if out[2] != 0 || out[3] != 0 {
		t.Fatalf("unmapped channels should be silence, got %v", out[2:])
	}
}

func TestApplyMalformedInputIsLocated(t *testing.T) {
	stereo, _ := Lookup("stereo")
	m, _ := DefaultMap(stereo, stereo)
	_, err := m.Apply([]float32{1}, 1, 7, 12345)
	if err == nil {
		t.Fatal("expected error for short input")
	}
	f, ok := fault.As(err)
	if !ok {
		t.Fatalf("not a fault: %v", err)
	}
	if f.TrackID != 7 || f.Offset != 12345 {
		t.Fatalf("fault not located: %+v", f)
	}
}

func TestRateConverterIntegralOnly(t *testing.T) {
	if _, err := NewRateConverter(48000, 24000); err != nil {
		t.Fatalf("48k->24k should be allowed: %v", err)
	}
	rc, err := NewRateConverter(24000, 48000)
	if err != nil {
		t.Fatalf("24k->48k: %v", err)
	}
	if got := rc.Resample(100); got != 200 {
		t.Fatalf("resample(100) = %d, want 200", got)
	}
	_, err = NewRateConverter(44100, 48000)
	if err == nil {
		t.Fatal("44.1k->48k is non-integral and must be rejected")
	}
	var f *fault.Fault
	if !errors.As(err, &f) || f.Kind != fault.KindSampleRate {
		t.Fatalf("expected sample_rate fault, got %v", err)
	}
}

func TestLocateAttachesTrackAndOffset(t *testing.T) {
	_, err := NewRateConverter(44100, 48000)
	loc := Locate(err, 9, 555)
	f, ok := fault.As(loc)
	if !ok {
		t.Fatal("Locate produced a non-fault")
	}
	if f.TrackID != 9 || f.Offset != 555 {
		t.Fatalf("fault not located: %+v", f)
	}
}
