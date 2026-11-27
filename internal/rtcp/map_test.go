package rtcp

import (
	"testing"

	"mediarebuild/internal/model"
)

func TestAnchorMapsExactly(t *testing.T) {
	m := NewMapper()
	m.AddSenderReport(100, 5_000_000_000, 90000)
	got, err := m.Map(100, 90000, 90000)
	if err != nil {
		t.Fatal(err)
	}
	if got != model.MediaTime(5_000_000_000) {
		t.Fatalf("anchor mapped to %d, want 5e9", got)
	}
}

func TestForwardDelta(t *testing.T) {
	m := NewMapper()
	m.AddSenderReport(1, 0, 1000)
	got, err := m.Map(1, 90000, 1000+90000) // +1s
	if err != nil {
		t.Fatal(err)
	}
	if got != model.MediaTime(1_000_000_000) {
		t.Fatalf("got %d ns, want 1e9", got)
	}
}

func TestMissingAnchorIsStructuredError(t *testing.T) {
	m := NewMapper()
	_, err := m.Map(42, 90000, 0)
	d, ok := err.(model.Diag)
	if !ok {
		t.Fatalf("err type %T, want model.Diag", err)
	}
	if d.Code != "no-mapping" || d.Recoverable {
		t.Fatalf("diag = %+v", d)
	}
}

func TestWrapAround(t *testing.T) {
	m := NewMapper()
	m.AddSenderReport(7, 0, 0xFFFFFFF0)
	// walk across the wrap in small steps
	var last model.MediaTime
	for ts := uint32(0xFFFFFFF0); ; ts += 100 {
		got, err := m.Map(7, 90000, ts)
		if err != nil {
			t.Fatalf("ts=%08x: %v", ts, err)
		}
		if got < last {
			t.Fatalf("time went backwards at ts=%08x", ts)
		}
		last = got
		if ts > 200 {
			break
		}
	}
}

func TestAmbiguousJump(t *testing.T) {
	m := NewMapper()
	m.AddSenderReport(9, 0, 0)
	_, err := m.Map(9, 90000, 1<<31)
	d, ok := err.(model.Diag)
	if !ok || d.Code != "ambiguous-wrap" {
		t.Fatalf("err = %v, want ambiguous-wrap diag", err)
	}
}

func TestInvalidTimescale(t *testing.T) {
	m := NewMapper()
	m.AddSenderReport(3, 0, 0)
	if _, err := m.Map(3, 0, 10); err == nil {
		t.Fatal("expected error for zero timescale")
	}
}
