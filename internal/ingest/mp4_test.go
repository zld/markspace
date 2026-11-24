package ingest

import (
	"encoding/binary"
	"math"
	"path/filepath"
	"testing"
)

func TestWriteAndReadFixtureMP4(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixture.mp4")
	rate, channels := 48000, 2
	frames := 4800
	pcm := make([]byte, frames*channels*2)
	for i := 0; i < frames; i++ {
		v := int16(0.2 * math.Sin(2*math.Pi*440*float64(i)/float64(rate)) * 32767)
		for c := 0; c < channels; c++ {
			binary.LittleEndian.PutUint16(pcm[(i*channels+c)*2:], uint16(v))
		}
	}
	err := WriteFixtureMP4(path, []FixtureTrack{{ID: 7, Rate: rate, Channels: channels, PCM: pcm}})
	if err != nil {
		t.Fatal(err)
	}
	tl, err := ReadMP4Timeline(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(tl.Tracks) != 1 || tl.Tracks[0].ID != 7 {
		t.Fatalf("unexpected tracks: %+v", tl.Tracks)
	}
	tr := tl.Tracks[0]
	if tr.Timescale != uint32(rate) || tr.Channels != 2 || tr.SampleRate != rate {
		t.Fatalf("metadata not round-tripped: %+v", tr)
	}
	if diff := tr.DurationNs - int64(frames)*1e9/int64(rate); diff > 2_000_000 || diff < -2_000_000 {
		t.Fatalf("duration ns %d too far from frame duration", tr.DurationNs)
	}
	raw, got, err := ReadMP4PCMS16(path, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != len(pcm) || got.PCMBytes != int64(len(pcm)) {
		t.Fatalf("pcm length %d/%d", len(raw), len(pcm))
	}
	if string(raw) != string(pcm) {
		t.Fatal("PCM payload mismatch")
	}
}
