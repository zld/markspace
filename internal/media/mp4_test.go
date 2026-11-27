package media

import (
	"bytes"
	"math"
	"testing"

	"example.com/assetpack/internal/audio"
)

func pcmAt(rate int, layout audio.Layout, frames int) audio.PCM {
	f := audio.Format{Rate: rate, Layout: layout}
	p := audio.Silence(f, frames)
	for c := range p.Channels {
		for i := range p.Channels[c] {
			p.Channels[c][i] = 0.25 * math.Sin(2*math.Pi*440*float64(i)/float64(rate)+float64(c))
		}
	}
	return p
}

func TestWriteAndReadIndex(t *testing.T) {
	p := pcmAt(48000, audio.LayoutStereo, 50013)
	data, ix, err := Write(p, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Fatal("empty container")
	}
	back, err := ReadIndex(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("index re-read failed: %v", err)
	}
	if back.Rate != 48000 || back.Channels != 2 || back.Frames != 50013 {
		t.Fatalf("index mismatch: %+v", back)
	}
	if ix.Digest == "" {
		t.Fatal("writer did not stamp a digest")
	}
	if ix.Samples != 11 || ix.Chunks != 1 {
		t.Fatalf("sample layout: samples=%d chunks=%d", ix.Samples, ix.Chunks)
	}
	// Verify that the stored chunk offset points at the mdat payload, not
	// at a zero placeholder.
	off := chunkOffset(t, data)
	mdat := bytes.Index(data, []byte("mdat"))
	if mdat < 0 || int(off) != mdat+4 {
		t.Fatalf("chunk offset %d, mdat payload at %d", off, mdat+4)
	}
}

func TestIndexValidateRejectsMismatch(t *testing.T) {
	p := pcmAt(48000, audio.LayoutMono, 1000)
	data, ix, err := Write(p, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := ix.Validate(1000, 48000, data); err != nil {
		t.Fatalf("valid container rejected: %v", err)
	}
	if err := ix.Validate(999, 48000, data); err == nil {
		t.Fatal("frame mismatch accepted")
	}
	if err := ix.Validate(1000, 44100, data); err == nil {
		t.Fatal("rate mismatch accepted")
	}
	bad := append([]byte(nil), data...)
	bad[len(bad)-1] ^= 0xFF
	if err := ix.Validate(1000, 48000, bad); err == nil {
		t.Fatal("digest mismatch accepted")
	}
}

func TestWriteEmptyRejected(t *testing.T) {
	p := audio.Silence(audio.Format{Rate: 48000, Layout: audio.LayoutMono}, 0)
	if _, _, err := Write(p, 0); err == nil {
		t.Fatal("empty container accepted")
	}
}

func TestMonoLayoutIndex(t *testing.T) {
	p := pcmAt(44100, audio.LayoutMono, 44100)
	data, ix, err := Write(p, 0)
	if err != nil {
		t.Fatal(err)
	}
	back, err := ReadIndex(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if back.Channels != 1 || back.Rate != 44100 || back.Frames != 44100 {
		t.Fatalf("mono index wrong: %+v", back)
	}
	if ix.SampleBytes != 2 {
		t.Fatalf("bytes per frame %d", ix.SampleBytes)
	}
}

func TestWriteIsDeterministic(t *testing.T) {
	p := pcmAt(48000, audio.LayoutStereo, 9600)
	a, aix, err := Write(p, 0)
	if err != nil {
		t.Fatal(err)
	}
	b, bix, err := Write(p, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) || aix.Digest != bix.Digest {
		t.Fatal("container output is not deterministic")
	}
}

func chunkOffset(t *testing.T, data []byte) uint32 {
	t.Helper()
	idx := bytes.Index(data, []byte("stco"))
	if idx < 0 {
		t.Fatal("no stco")
	}
	pos := idx + 4 + 4 + 4
	return uint32(data[pos])<<24 | uint32(data[pos+1])<<16 | uint32(data[pos+2])<<8 | uint32(data[pos+3])
}
