package index

import (
	"testing"

	"continuity/internal/wav"
)

func TestContentDigestStableAndSensitive(t *testing.T) {
	layout, _ := wav.NewLayout(2, 16, 48000)
	pcm := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	in := ContentInput{PCM: pcm, Layout: layout, StartSample: 0, EndSample: 4}
	h1 := ContentDigest(in)
	h2 := ContentDigest(in)
	if h1 != h2 {
		t.Fatal("identical content produced different digests")
	}
	// A single changed byte must change the digest.
	changed := append([]byte(nil), pcm...)
	changed[0] ^= 0xFF
	if ContentDigest(ContentInput{PCM: changed, Layout: layout, StartSample: 0, EndSample: 4}) == h1 {
		t.Fatal("changed PCM did not change digest")
	}
	// Same bytes, different sample range must change the digest.
	if ContentDigest(ContentInput{PCM: pcm, Layout: layout, StartSample: 8, EndSample: 12}) == h1 {
		t.Fatal("sample range was not included in digest")
	}
	// Same bytes, different layout must change the digest.
	mono, _ := wav.NewLayout(1, 16, 48000)
	if ContentDigest(ContentInput{PCM: pcm, Layout: mono, StartSample: 0, EndSample: 4}) == h1 {
		t.Fatal("layout was not included in digest")
	}
}

func TestPeakLoudnessRange(t *testing.T) {
	// Full-scale 16-bit tone must be 0 dBFS; silence must be the floor.
	full := []byte{0xFF, 0x7F, 0x00, 0x80} // +32767 and -32768
	if got := PeakLoudness(full); got.PeakDBFS > 0.01 || got.PeakDBFS < -0.01 {
		t.Fatalf("full scale peak=%f want ~0", got.PeakDBFS)
	}
	if got := PeakLoudness(make([]byte, 4)); got.PeakDBFS != -96 {
		t.Fatalf("silence peak=%f want -96", got.PeakDBFS)
	}
	// Determinism.
	a := PeakLoudness([]byte{0x10, 0x27, 0x00, 0x40})
	b := PeakLoudness([]byte{0x10, 0x27, 0x00, 0x40})
	if a != b {
		t.Fatal("loudness not deterministic")
	}
}
