package fixture

import (
	"bytes"
	"os"
	"testing"

	"github.com/abema/go-mp4"
)

func TestFixturesAreDeterministic(t *testing.T) {
	opts := DefaultOptions("det", KindNormal)
	opts.SampleCount = 7
	a, err := encode(opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := encode(opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("fixture encoding is not deterministic")
	}
}

func TestFixturesProbe(t *testing.T) {
	dir := t.TempDir()
	samples, err := BuildAll(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 9 {
		t.Fatalf("samples = %d", len(samples))
	}
	for _, s := range samples {
		switch s.Kind {
		case KindTruncated, KindTruncatedHeader, KindBadLength:
			continue
		}
		f, err := os.Open(s.Path)
		if err != nil {
			t.Fatal(err)
		}
		info, err := mp4.Probe(f)
		f.Close()
		if err != nil {
			t.Fatalf("%s: %v", s.Name, err)
		}
		if info.Timescale != s.Timescale {
			t.Fatalf("%s: movie timescale %d", s.Name, info.Timescale)
		}
		if len(info.Tracks) != 1 || info.Tracks[0].Timescale != s.MediaTimescale {
			t.Fatalf("%s: track layout unexpected", s.Name)
		}
	}
}

func TestCorruptionOffsets(t *testing.T) {
	dir := t.TempDir()
	opts := DefaultOptions("bad", KindBadLength)
	opts.SampleCount = 5
	s, err := BuildCorrupt(dir, opts, KindBadLength)
	if err != nil {
		t.Fatal(err)
	}
	if s.CorruptAt <= 0 {
		t.Fatalf("corrupt offset = %d", s.CorruptAt)
	}
	data, err := os.ReadFile(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if got := uint32(data[s.CorruptAt])<<24 | uint32(data[s.CorruptAt+1])<<16 | uint32(data[s.CorruptAt+2])<<8 | uint32(data[s.CorruptAt+3]); got != 0x7FFFFFF0 {
		t.Fatalf("patched size = %#x", got)
	}
}

func TestTruncatedHeaderOffset(t *testing.T) {
	dir := t.TempDir()
	opts := DefaultOptions("th", KindTruncatedHeader)
	opts.SampleCount = 5
	s, err := BuildCorrupt(dir, opts, KindTruncatedHeader)
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() != s.CorruptAt+5 {
		t.Fatalf("size = %d want %d", st.Size(), s.CorruptAt+5)
	}
}
