package fixture

import (
	"bytes"
	"os"
	"testing"

	"liveregress/internal/ts"
	"liveregress/internal/wav"
)

func TestBuildWAVIsReadable(t *testing.T) {
	for _, bits := range []int{16, 24, 32} {
		data, err := BuildWAV(WAVSpec{Channels: 3, Bits: bits, SampleRate: 48000, Frames: 1000, Seed: 3})
		if err != nil {
			t.Fatal(err)
		}
		if data.DataOffset <= 0 || data.Frames != 1000 || data.BlockAlign != 3*bits/8 {
			t.Fatalf("%d-bit meta = %+v", bits, data)
		}
		if int64(len(data.Bytes)) != data.DataOffset+data.DataSize {
			t.Fatalf("%d-bit size mismatch: len=%d offset=%d data=%d", bits, len(data.Bytes), data.DataOffset, data.DataSize)
		}
	}
}

func TestBuildTSIsDemuxable(t *testing.T) {
	var aus []TSAU
	for i := 0; i < 5; i++ {
		aus = append(aus, TSAU{PTS: uint64(i) * 3000, Key: i == 0, Payload: AnnexBAU(i == 0, 400, i)})
	}
	raw := BuildTS(TSSpec{VideoPID: 0x101, AudioPID: 0x102, AUs: aus})
	d, err := ts.DemuxFile(bytes.NewReader(raw), "generated.ts")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Tracks) != 1 {
		t.Fatalf("tracks = %d", len(d.Tracks))
	}
	if len(d.Tracks[0].Samples) != 5 {
		t.Fatalf("samples = %d (truncated=%d faults=%v)", len(d.Tracks[0].Samples), d.Tracks[0].TruncatedSamples, d.Faults)
	}
	if !d.Tracks[0].Samples[0].Keyframe || d.Tracks[0].Samples[1].Keyframe {
		t.Fatal("keyframe flags wrong")
	}
}

func TestBuildWAVTruncationDetected(t *testing.T) {
	data, err := BuildWAV(WAVSpec{Channels: 3, Bits: 24, SampleRate: 48000, Frames: 100, Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	cut := data.Bytes[:len(data.Bytes)-5]
	dir := t.TempDir()
	path := dir + "/t.wav"
	if err := os.WriteFile(path, cut, 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := wav.Open(path, "t.wav")
	if err == nil {
		t.Fatal("expected truncation fault")
	}
	if info == nil || info.Frames != 99 {
		t.Fatalf("complete prefix frames = %+v", info)
	}
}
