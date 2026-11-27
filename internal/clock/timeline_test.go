package clock

import (
	"errors"
	"testing"

	"example.com/assetpack/internal/fault"
)

func TestSampleNanosRoundTrip(t *testing.T) {
	tl, err := New(48000)
	if err != nil {
		t.Fatal(err)
	}
	for _, samples := range []int64{0, 480, 48000, 96001, 1234567} {
		nanos := tl.SamplesToNanos(samples)
		if back := tl.NanosToSamples(nanos); back != samples {
			t.Fatalf("round trip %d -> %d -> %d", samples, nanos, back)
		}
	}
}

func TestRescaleStable(t *testing.T) {
	// A whole second at 44100 rescales exactly to 48000 samples.
	got, err := Rescale(44100, 44100, 48000)
	if err != nil {
		t.Fatal(err)
	}
	if got != 48000 {
		t.Fatalf("rescale got %d", got)
	}
	// Half a second survives then is rescaled again without drift.
	pos, err := Rescale(22050, 44100, 48000)
	if err != nil {
		t.Fatal(err)
	}
	if pos != 24000 {
		t.Fatalf("rescale got %d", pos)
	}
}

func TestInvalidRates(t *testing.T) {
	_, err := New(0)
	var f *fault.Fault
	if !errors.As(err, &f) || f.Kind != fault.KindInvalidArg {
		t.Fatalf("expected invalid-arg fault, got %v", err)
	}
	if _, err := New(-5); err == nil {
		t.Fatal("expected error for negative rate")
	}
	if _, err := Rescale(1, 0, 48000); err == nil {
		t.Fatal("expected rescale rate error")
	}
	if _, err := Rescale(1<<62, 1, 2); err == nil {
		t.Fatal("expected overflow error")
	}
}

func TestPosition(t *testing.T) {
	tl, _ := New(90000)
	p := tl.NewPosition(188, 90000)
	if p.Nanos != 1e9 || !p.Valid() {
		t.Fatalf("bad position %+v", p)
	}
	if got := p.String(); got != "offset=188 sample=90000 nanos=1000000000" {
		t.Fatalf("unexpected string %q", got)
	}
	if (Position{Offset: -1}).Valid() {
		t.Fatal("negative offset must be invalid")
	}
}
