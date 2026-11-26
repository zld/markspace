package wav

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"example.com/mediavault/internal/fault"
	"example.com/mediavault/internal/fixtures"
)

func scan(t *testing.T, b []byte) (*Info, error) {
	t.Helper()
	return Scan(bytes.NewReader(b), int64(len(b)))
}

func TestScanAcceptsEverySupportedLayout(t *testing.T) {
	for _, sampleRate := range []int{8000, 16000, 44100, 48000, 96000, 192000} {
		for _, channels := range []int{1, 2, 6, 8} {
			for _, bits := range []int{16, 24, 32} {
				b := fixtures.WAV(fixtures.WAVOptions{
					SampleRate: sampleRate, Channels: channels, Bits: bits,
					Frames: 97, BlockFrames: 32, ModifyBlock: -1,
				})
				info, err := scan(t, b)
				if err != nil {
					t.Fatalf("%d Hz %d ch %d bit: %v", sampleRate, channels, bits, err)
				}
				if info.Frames != 97 {
					t.Fatalf("%d Hz %d ch %d bit: frames = %d", sampleRate, channels, bits, info.Frames)
				}
				if info.Format.FrameBytes() != channels*bits/8 {
					t.Fatalf("frame bytes = %d", info.Format.FrameBytes())
				}
				if len(info.SourceDigest) != 64 || len(info.ContainerDigest) != 64 {
					t.Fatalf("digests not 64 hex: %q %q", info.SourceDigest, info.ContainerDigest)
				}
			}
		}
	}
}

func TestScanRejectsTruncatedRIFF(t *testing.T) {
	full := fixtures.WAV(fixtures.WAVOptions{SampleRate: 8000, Frames: 512, BlockFrames: 128, ModifyBlock: -1})
	cut := full[:len(full)-40]
	_, err := scan(t, cut)
	if fault.CodeOf(err) != fault.CodeRIFFLength {
		t.Fatalf("code = %q (%v)", fault.CodeOf(err), err)
	}
	if fault.StageOf(err) != fault.StageScanned {
		t.Fatalf("stage = %q", fault.StageOf(err))
	}
	if fault.OffsetOf(err) != 4 {
		t.Fatalf("offset = %d, want 4 (the RIFF size field)", fault.OffsetOf(err))
	}
}

func TestScanRejectsUnalignedData(t *testing.T) {
	b := fixtures.WAV(fixtures.WAVOptions{
		SampleRate: 8000, Channels: 2, Bits: 16, Frames: 256, BlockFrames: 64, ModifyBlock: -1, Unaligned: 3,
	})
	_, err := scan(t, b)
	if fault.CodeOf(err) != fault.CodeFrameAlign {
		t.Fatalf("code = %q (%v)", fault.CodeOf(err), err)
	}
	if fault.OffsetOf(err) < 0 {
		t.Fatal("frame alignment fault must carry an offset")
	}
}

func TestScanRejectsChunkThatRunsPastRIFF(t *testing.T) {
	b := fixtures.WAV(fixtures.WAVOptions{SampleRate: 8000, Frames: 256, BlockFrames: 64, ModifyBlock: -1})
	// Inflate the data chunk size by 200 bytes; the RIFF length is untouched.
	dataAt := bytes.Index(b, []byte("data"))
	if dataAt < 0 {
		t.Fatal("fixture has no data chunk")
	}
	size := uint32(len(b) - dataAt - 8 + 200)
	b[dataAt+4] = byte(size)
	b[dataAt+5] = byte(size >> 8)
	b[dataAt+6] = byte(size >> 16)
	b[dataAt+7] = byte(size >> 24)
	_, err := scan(t, b)
	if fault.CodeOf(err) != fault.CodeChunkBoundary {
		t.Fatalf("code = %q (%v)", fault.CodeOf(err), err)
	}
}

func TestScanRejectsUnsupportedFormats(t *testing.T) {
	cases := []struct {
		name string
		opts fixtures.WAVOptions
	}{
		{"nine-channels", fixtures.WAVOptions{SampleRate: 8000, Channels: 9, Bits: 16, Frames: 64, BlockFrames: 16}},
		{"eight-bit", fixtures.WAVOptions{SampleRate: 8000, Channels: 1, Bits: 8, Frames: 64, BlockFrames: 16}},
		{"low-rate", fixtures.WAVOptions{SampleRate: 4000, Channels: 1, Bits: 16, Frames: 64, BlockFrames: 16}},
		{"high-rate", fixtures.WAVOptions{SampleRate: 384000, Channels: 1, Bits: 16, Frames: 64, BlockFrames: 16}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := scan(t, fixtures.WAV(c.opts))
			if fault.CodeOf(err) != fault.CodeFormat {
				t.Fatalf("code = %q (%v)", fault.CodeOf(err), err)
			}
		})
	}
}

func TestReaderIsFrameAlignedAndBounded(t *testing.T) {
	b := fixtures.WAV(fixtures.WAVOptions{
		SampleRate: 48000, Channels: 3, Bits: 24, Frames: 1000, BlockFrames: 256, ModifyBlock: -1,
	})
	info, err := scan(t, b)
	if err != nil {
		t.Fatal(err)
	}
	rd, err := NewReader(bytes.NewReader(b), info)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for {
		pcm, frames, err := rd.Next(128)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		if int64(len(pcm)) != frames*int64(info.Format.FrameBytes()) {
			t.Fatalf("block %d frames not frame aligned: %d bytes / %d frames", total, len(pcm), frames)
		}
		if len(pcm) > MaxBlockBytes {
			t.Fatalf("block exceeds working memory envelope: %d", len(pcm))
		}
		total += frames
	}
	if total != 1000 {
		t.Fatalf("read %d frames, want 1000", total)
	}
}

func TestReaderResumesAtExactFrame(t *testing.T) {
	b := fixtures.WAV(fixtures.WAVOptions{SampleRate: 8000, Channels: 1, Bits: 16, Frames: 512, BlockFrames: 64, ModifyBlock: -1})
	info, err := scan(t, b)
	if err != nil {
		t.Fatal(err)
	}
	rd, err := NewReader(bytes.NewReader(b), info)
	if err != nil {
		t.Fatal(err)
	}
	all, err := rd.ReadFrames(0, 512)
	if err != nil {
		t.Fatal(err)
	}
	mid, err := rd.ReadFrames(128, 64)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(mid, all[128*2:192*2]) {
		t.Fatal("resumed range does not match the same frames of the full read")
	}
	if _, err := rd.ReadFrames(-1, 10); fault.CodeOf(err) != fault.CodeArgs {
		t.Fatalf("negative frame must be a usage fault, got %v", err)
	}
	if _, err := rd.ReadFrames(500, 100); fault.CodeOf(err) != fault.CodeArgs {
		t.Fatalf("range past the end must be a usage fault, got %v", err)
	}
}
