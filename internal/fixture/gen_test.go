package fixture

import (
	"bytes"
	"testing"
)

func TestDeterministic(t *testing.T) {
	a := Generate(17, "")
	b := Generate(17, "")
	if !bytes.Equal(a.Matroska, b.Matroska) || !bytes.Equal(a.MP4Init, b.MP4Init) {
		t.Fatal("same seed produced different container bytes")
	}
	if len(a.RTP) != len(b.RTP) {
		t.Fatal("same seed produced different packet counts")
	}
	for i := range a.RTP {
		if !bytes.Equal(a.RTP[i], b.RTP[i]) {
			t.Fatalf("packet %d differs", i)
		}
	}
}

func TestSeedVariesOutput(t *testing.T) {
	a := Generate(17, "")
	b := Generate(18, "")
	if bytes.Equal(a.Matroska, b.Matroska) {
		t.Fatal("different seeds produced identical Matroska payloads")
	}
}

func TestCorruptVariants(t *testing.T) {
	clean := Generate(17, "")
	trunc := Generate(17, "truncated-block")
	if bytes.Equal(trunc.Matroska, clean.Matroska) {
		t.Fatal("truncated variant identical to clean")
	}
	// The truncated variant uses one extra unknown-size header (Segment,
	// +6 bytes vs known-size) and is then cut 7 bytes short.
	unk := Generate(17, "unknown-length-boundary")
	if len(trunc.Matroska) != len(unk.Matroska)-1 {
		t.Fatalf("truncated length %d, want %d", len(trunc.Matroska), len(unk.Matroska)-1)
	}
	if bytes.Equal(unk.Matroska, clean.Matroska) {
		t.Fatal("unknown-length variant identical to clean")
	}
	// RTP and MP4 inputs are unaffected by container corruption.
	if !bytes.Equal(trunc.MP4Init, clean.MP4Init) {
		t.Fatal("corruption leaked into MP4 init")
	}
}
