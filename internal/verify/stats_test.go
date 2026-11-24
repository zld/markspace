package verify_test

import (
	"encoding/binary"
	"testing"

	"media-archiver/internal/media"
	"media-archiver/internal/verify"
)

func TestComputePCM16StereoStatsAndBitrate(t *testing.T) {
	format := media.PCMFormat{
		AudioFormat: 1, Channels: 2, SampleRate: 48000,
		ByteRate: 192000, BlockAlign: 4, BitsPerSample: 16,
	}
	pcm := make([]byte, 8)
	binary.LittleEndian.PutUint16(pcm[0:2], uint16(32767))
	binary.LittleEndian.PutUint16(pcm[2:4], 0x8000)
	binary.LittleEndian.PutUint16(pcm[4:6], 100)
	binary.LittleEndian.PutUint16(pcm[6:8], 200)

	stats, err := verify.Compute(format, pcm, 1)
	if err != nil {
		t.Fatal(err)
	}
	if stats.TargetBitrateBPS != 1_536_000 {
		t.Fatalf("target=%d", stats.TargetBitrateBPS)
	}
	if stats.ActualBitrateBPS != 1_536_000 {
		t.Fatalf("actual=%v", stats.ActualBitrateBPS)
	}
	if stats.BitrateDeviationPct != 0 || !stats.BitrateWithinTolerance(0.1) {
		t.Fatalf("deviation=%v", stats.BitrateDeviationPct)
	}
	if stats.Frames != 2 || stats.Channels != 2 || stats.BadBlocks != 1 {
		t.Fatalf("counts wrong: %+v", stats)
	}
	if stats.Peak != 32768 || stats.ClippedSamples != 2 {
		t.Fatalf("peak=%d clipped=%d", stats.Peak, stats.ClippedSamples)
	}
}

func TestComputeRejectsNonFrameAlignedPCM(t *testing.T) {
	format := media.PCMFormat{
		AudioFormat: 1, Channels: 2, SampleRate: 48000,
		ByteRate: 192000, BlockAlign: 4, BitsPerSample: 16,
	}
	if _, err := verify.Compute(format, []byte{1, 2, 3}, 0); err == nil {
		t.Fatal("expected frame alignment error")
	}
}

func TestDecode24BitSample(t *testing.T) {
	format := media.PCMFormat{
		AudioFormat: 1, Channels: 1, SampleRate: 48000,
		ByteRate: 144000, BlockAlign: 3, BitsPerSample: 24,
	}
	// -1 in signed 24-bit little endian.
	stats, err := verify.Compute(format, []byte{0xff, 0xff, 0xff}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Frames != 1 || stats.Peak != 1 {
		t.Fatalf("frames=%d peak=%d", stats.Frames, stats.Peak)
	}
}
