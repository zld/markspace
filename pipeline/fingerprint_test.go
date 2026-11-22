package pipeline

import (
	"testing"

	"mediacore/index"
	"mediacore/media"
)

func TestFingerprintSameContentDifferentTimeGroups(t *testing.T) {
	d, _ := NewDeduper(DefaultFingerprintConfig())
	payload := make([]byte, 64)
	for i := range payload {
		payload[i] = byte(i)
	}
	a := &media.Frame{Width: 8, Height: 8, Payload: append([]byte(nil), payload...)}
	b := &media.Frame{Width: 8, Height: 8, Payload: append([]byte(nil), payload...)}
	e1 := index.Entry{Seq: 1, Ticks: 0, Width: 8, Height: 8}
	e2 := index.Entry{Seq: 2, Ticks: 3600, Width: 8, Height: 8}
	if _, dup, _ := d.Add(e1, a); dup {
		t.Fatal("first frame should not be a duplicate")
	}
	if _, dup, _ := d.Add(e2, b); !dup {
		t.Fatal("identical content at a different time should group")
	}
	groups := d.DuplicateGroups()
	if len(groups) != 1 || groups[0].Count != 2 {
		t.Fatalf("groups=%+v", groups)
	}
	if groups[0].Frames[1].Ticks != 3600 {
		t.Fatal("duplicate should retain its own timestamp")
	}
}

func TestFingerprintDifferentContentNotGrouped(t *testing.T) {
	d, _ := NewDeduper(DefaultFingerprintConfig())
	a := &media.Frame{Width: 8, Height: 8, Payload: make([]byte, 64)}
	b := &media.Frame{Width: 8, Height: 8, Payload: make([]byte, 64)}
	b.Payload[0] = 1
	e := index.Entry{Width: 8, Height: 8}
	d.Add(e, a)
	if _, dup, _ := d.Add(e, b); dup {
		t.Fatal("distinct content must not be merged")
	}
	if n := d.DuplicateFrameCount(); n != 0 {
		t.Fatalf("duplicate frames=%d want 0", n)
	}
}

func TestBlockNormalizeGridLargerThanImage(t *testing.T) {
	cfg := FingerprintConfig{Mode: NormalizeBlock, Grid: 32, Quantum: 8}
	f := &media.Frame{Width: 4, Height: 4, Payload: make([]byte, 16)}
	for i := range f.Payload {
		f.Payload[i] = 200
	}
	fp := cfg.Compute(f)
	if len(fp.Canonical) == 0 {
		t.Fatal("block normalize produced no output for a tiny image")
	}
}

func TestFingerprintConfigValidation(t *testing.T) {
	if err := (FingerprintConfig{Mode: "nope"}).Validate(); err == nil {
		t.Fatal("unknown mode must be rejected")
	}
	if err := (FingerprintConfig{Mode: NormalizeBlock, Grid: 0, Quantum: 8}).Validate(); err == nil {
		t.Fatal("zero grid must be rejected")
	}
	if err := (FingerprintConfig{Mode: NormalizeBlock, Grid: 8, Quantum: 9}).Validate(); err == nil {
		t.Fatal("quantum out of range must be rejected")
	}
}
