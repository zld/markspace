package fingerprint

import (
	"testing"

	"medialib/internal/audio"
)

func TestHashDeterministic(t *testing.T) {
	p := audio.PCM{Rate: 48000, Channels: 2, Samples: []float32{0.1, -0.2, 0.999, -1.0}}
	if Hash(p) != Hash(p) {
		t.Fatal("hash not deterministic")
	}
	q := p
	q.Samples = append([]float32(nil), p.Samples...)
	q.Samples[0] += 0.001
	if Hash(p) == Hash(q) {
		t.Fatal("distinct content hashed equal")
	}
}

func TestNormalizeBindsFormat(t *testing.T) {
	a := audio.PCM{Rate: 48000, Channels: 1, Samples: []float32{0, 0}}
	b := audio.PCM{Rate: 44100, Channels: 1, Samples: []float32{0, 0}}
	if Hash(a) == Hash(b) {
		t.Fatal("format not bound to hash")
	}
	if len(Normalize(a)) == 0 {
		t.Fatal("empty normalization")
	}
}

func TestNormalizeClips(t *testing.T) {
	p := audio.PCM{Rate: 48000, Channels: 1, Samples: []float32{2.5, -2.5}}
	n := Normalize(p)
	if len(n) == 0 {
		t.Fatal("empty")
	}
	// Clipped values must hash identically to exact full scale.
	full := audio.PCM{Rate: 48000, Channels: 1, Samples: []float32{1.0, -1.0}}
	if Hash(p) != Hash(full) {
		t.Fatal("clipping not canonical")
	}
}
