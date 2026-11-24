package media

import (
	"encoding/binary"
	"errors"
	"testing"

	"mediasync/internal/clock"
)

// buildWAV assembles a little-endian PCM WAV byte slice.
func buildWAV(channels, rate, bits int, frames int, extraTrailing int) []byte {
	ba := channels * bits / 8
	dataSize := frames*ba + extraTrailing
	buf := make([]byte, 0, 44+dataSize)
	put := func(s string) { buf = append(buf, []byte(s)...) }
	putU32 := func(v uint32) {
		tmp := make([]byte, 4)
		binary.LittleEndian.PutUint32(tmp, v)
		buf = append(buf, tmp...)
	}
	putU16 := func(v uint16) {
		tmp := make([]byte, 2)
		binary.LittleEndian.PutUint16(tmp, v)
		buf = append(buf, tmp...)
	}
	put("RIFF")
	putU32(uint32(36 + dataSize))
	put("WAVE")
	put("fmt ")
	putU32(16)
	putU16(1)
	putU16(uint16(channels))
	putU32(uint32(rate))
	putU32(uint32(rate * ba))
	putU16(uint16(ba))
	putU16(uint16(bits))
	put("data")
	putU32(uint32(dataSize))
	pcm := make([]byte, dataSize)
	for i := range pcm {
		pcm[i] = byte(i % 251)
	}
	buf = append(buf, pcm...)
	return buf
}

func TestParseWAV24Bit8Channels(t *testing.T) {
	raw := buildWAV(8, 48000, 24, 100, 0)
	w, err := ParseWAV(raw)
	if err != nil {
		t.Fatal(err)
	}
	if w.Header.Channels != 8 || w.Header.BitsPerSample != 24 || w.Header.BlockAlign != 24 {
		t.Fatalf("header mismatch: %+v", w.Header)
	}
	if w.FrameCount() != 100 || w.TailBytes() != 0 {
		t.Fatalf("frames=%d tail=%d", w.FrameCount(), w.TailBytes())
	}
}

func TestParseWAVTruncationReportsOffset(t *testing.T) {
	raw := buildWAV(2, 48000, 16, 100, 0)
	cut := len(raw) - 37 // drop 37 payload bytes mid-frame region
	w, err := ParseWAV(raw[:cut])
	if err == nil {
		t.Fatal("expected truncation error")
	}
	var me *clock.MediaError
	if !errors.As(err, &me) || me.Code != clock.ErrTruncated {
		t.Fatalf("expected structured truncation, got %v", err)
	}
	if me.Missing != 37 || me.Offset != int64(cut) {
		t.Fatalf("offset=%d missing=%d", me.Offset, me.Missing)
	}
	if !w.Truncated || int64(len(w.PCM)) != w.Header.DataSize-37 {
		t.Fatalf("surviving prefix not retained: %+v", w)
	}
}

func TestParseWAVPartialFinalFrame(t *testing.T) {
	raw := buildWAV(2, 48000, 16, 10, 3) // 3 dangling bytes
	w, err := ParseWAV(raw)
	if err == nil {
		t.Fatal("expected partial-frame error")
	}
	var me *clock.MediaError
	if !errors.As(err, &me) || me.Missing != 3 {
		t.Fatalf("want missing=3, got %v", err)
	}
	if w.FrameCount() != 10 {
		t.Fatalf("complete frames should remain readable: %d", w.FrameCount())
	}
}

func TestParseWAVRejectsBadConfig(t *testing.T) {
	cases := [][]byte{
		buildWAV(9, 48000, 16, 10, 0),   // too many channels
		buildWAV(2, 48000, 8, 10, 0),    // 8-bit unsupported
		[]byte("not a wav file at all"), // bad container
	}
	for i, c := range cases {
		if _, err := ParseWAV(c); err == nil {
			t.Errorf("case %d: expected error", i)
		}
	}
}

func TestSplitBlocksCrossBoundary(t *testing.T) {
	// blockAlign=4; odd target size forces a frame to span two blocks.
	pcm := make([]byte, 40) // 10 frames
	blocks, err := SplitBlocks(pcm, 4, 10)
	if err != nil {
		t.Fatal(err)
	}
	var total int
	for _, b := range blocks {
		if len(b.Frames)%4 != 0 {
			t.Fatalf("block %d frames not frame-aligned: %d", b.Index, len(b.Frames))
		}
		total += len(b.Frames)
	}
	if total != len(pcm) {
		t.Fatalf("recovered bytes=%d want %d", total, len(pcm))
	}
}

func TestSplitBlocksDanglingCarry(t *testing.T) {
	pcm := make([]byte, 23) // 5 frames + 3 dangling bytes
	blocks, err := SplitBlocks(pcm, 4, 8)
	if err == nil {
		t.Fatal("expected truncation error")
	}
	var me *clock.MediaError
	if !errors.As(err, &me) || me.Missing != 1 {
		t.Fatalf("want missing=1 byte, got %v", err)
	}
	if n := len(blocks); n == 0 || len(blocks[n-1].Carry) != 3 {
		t.Fatalf("carry not retained: %+v", blocks)
	}
}

func TestFrameAtBounds(t *testing.T) {
	pcm := make([]byte, 16)
	if _, err := FrameAt(pcm, 4, 4); err == nil {
		t.Fatal("frame beyond input must error, not read out of bounds")
	}
	if _, err := FrameAt(pcm, 4, 3); err != nil {
		t.Fatalf("last valid frame errored: %v", err)
	}
}
