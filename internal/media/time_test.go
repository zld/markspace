package media

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRateConverterNoDrift(t *testing.T) {
	// AAC at 44.1 kHz: 1024-sample frames mapped onto 48 kHz.
	c := NewRateConverter(44100)
	const frames = 1000
	var total uint64
	for i := 0; i < frames; i++ {
		n, _ := c.Convert(1024)
		total += n
	}
	want := uint64(uint64(frames) * 1024 * TimelineRate / 44100)
	if total != want && total != want+1 {
		t.Fatalf("drift: got %d samples, want %d±1 over %d frames", total, want, frames)
	}
	if c.Remainder() >= 44100 {
		t.Fatalf("remainder not bounded: %d", c.Remainder())
	}
	// Exact-rate conversion must be exact.
	ex := NewRateConverter(TimelineRate)
	for i := 0; i < 10; i++ {
		if n, _ := ex.Convert(480); n != 480 {
			t.Fatalf("exact-rate conversion returned %d", n)
		}
	}
}

func TestScaleAndRange(t *testing.T) {
	if got := Scale(1024, 44100); got != 1115 {
		t.Fatalf("Scale = %d, want 1115", got)
	}
	r := Range{Start: 100, End: 200}
	if r.Length() != 100 || !r.Valid() {
		t.Fatal("range basics broken")
	}
	if !r.Overlaps(Range{Start: 199, End: 300}) || r.Overlaps(Range{Start: 200, End: 300}) {
		t.Fatal("overlap predicate broken")
	}
}

func TestSafeOpenRejectsTraversalAndSymlink(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.pcm")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := SafeOpen(root, outside); err == nil {
		t.Fatal("traversal outside root was accepted")
	}
	good := filepath.Join(root, "in.rtp")
	if err := os.WriteFile(good, []byte("rtp"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, abs, err := SafeOpen(root, good)
	if err != nil {
		t.Fatalf("regular file rejected: %v", err)
	}
	f.Close()
	link := filepath.Join(root, "link.rtp")
	if err := os.Symlink(abs, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := SafeOpen(root, link); err == nil {
		t.Fatal("symlink input was accepted")
	}
}

func TestErrorLogDedup(t *testing.T) {
	l := NewErrorLog()
	l.Add(NewPendingError(ErrCorruptPacket, "bad", 7, 42))
	l.Add(NewPendingError(ErrCorruptPacket, "bad", 7, 42))
	if l.Len() != 1 || !l.Has(ErrCorruptPacket) {
		t.Fatal("dedup or lookup broken")
	}
}
