package rtpreplay

import (
	"testing"
)

func TestWriteMP4AACRoundTrip(t *testing.T) {
	asc := []byte{0x11, 0x90} // AAC-LC 48kHz stereo
	sizes := make([]uint32, 25)
	durs := make([]uint32, 25)
	for i := range sizes {
		sizes[i] = 200
		durs[i] = 1024
	}
	data, err := WriteMP4(AudioTrackSpec{
		Codec: CodecAAC, Timescale: 48000, Durations: durs, Sizes: sizes,
		AACASC: asc, SampleRate: 48000, Channels: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	tr, err := ReadMP4Track(data)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Codec != CodecAAC || tr.Timescale != 48000 {
		t.Fatalf("track=%+v", tr)
	}
	if len(tr.Samples) != 25 || tr.Samples[0].Duration != 1024 || tr.Samples[0].Size != 200 {
		t.Fatalf("samples=%d first=%+v", len(tr.Samples), tr.Samples[0])
	}
	if tr.AAC == nil || tr.AAC.SampleRate != 48000 || tr.AAC.SamplesPerFrame() != 1024 {
		t.Fatalf("aac=%+v", tr.AAC)
	}
	total, err := tr.TotalTicks()
	if err != nil || total != 25*1024 {
		t.Fatalf("total=%d err=%v", total, err)
	}
}

func TestWriteMP4VariableDurations(t *testing.T) {
	durs := []uint32{960, 960, 1920, 960, 2880}
	sizes := []uint32{80, 80, 160, 80, 240}
	data, err := WriteMP4(AudioTrackSpec{
		Codec: CodecOpus, Timescale: 48000, Durations: durs, Sizes: sizes,
		OpusPreSkip: 312, OpusRate: 48000, SampleRate: 48000, Channels: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	tr, err := ReadMP4Track(data)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Codec != CodecOpus || len(tr.Samples) != 5 {
		t.Fatalf("track=%+v", tr)
	}
	for i := range durs {
		if tr.Samples[i].Duration != durs[i] {
			t.Fatalf("sample %d duration=%d want %d", i, tr.Samples[i].Duration, durs[i])
		}
	}
	if tr.Opus == nil || tr.Opus.PreSkip != 312 {
		t.Fatalf("opus=%+v", tr.Opus)
	}
}

func TestWriteMP4RejectsBadSpec(t *testing.T) {
	if _, err := WriteMP4(AudioTrackSpec{Codec: CodecAAC}); err == nil {
		t.Fatal("expected error")
	}
	if _, err := WriteMP4(AudioTrackSpec{
		Codec: CodecAAC, Timescale: 48000, Durations: []uint32{1, 2}, Sizes: []uint32{1},
	}); err == nil {
		t.Fatal("expected length mismatch error")
	}
}
