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
	if len(samples) != 10 {
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
		if len(info.Tracks) == 0 || info.Tracks[0].Timescale != s.MediaTimescale {
			t.Fatalf("%s: track layout unexpected", s.Name)
		}
		if s.Kind == KindMultiTrack && len(info.Tracks) != 2 {
			t.Fatalf("%s: expected two tracks, got %d", s.Name, len(info.Tracks))
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

func TestMultiTrackProbes(t *testing.T) {
	dir := t.TempDir()
	opts := DefaultOptions("mt", KindNormal)
	opts.WithAudio = true
	opts.SampleCount = 30
	s, err := Build(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	info, err := mp4.Probe(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Tracks) != 2 {
		t.Fatalf("tracks = %d", len(info.Tracks))
	}
	if info.Tracks[0].Timescale != 90000 || info.Tracks[1].Timescale != 48000 {
		t.Fatalf("timescales = %d/%d", info.Tracks[0].Timescale, info.Tracks[1].Timescale)
	}
}
