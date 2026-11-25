package mp4

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"mediad/internal/clock"
)

func makeSegment(t *testing.T, frames uint64) Segment {
	t.Helper()
	specs := []TrackSpec{
		{TrackID: 1, BitsPerSample: 16, SampleRate: 48000},
		{TrackID: 2, BitsPerSample: 16, SampleRate: 48000},
	}
	pcm := make([][]byte, 2)
	for tr := range pcm {
		pcm[tr] = make([]byte, int(frames)*2)
		for i := range pcm[tr] {
			pcm[tr][i] = byte(int(frames)*tr + i*5 + 1)
		}
	}
	return Segment{
		Frames:     clock.FrameRange{Start: 0, End: frames},
		Tracks:     specs,
		PCM:        pcm,
		KeyframeAt: 0,
	}
}

func TestMuxAndReparseRoundTrip(t *testing.T) {
	seg := makeSegment(t, 64)
	path := filepath.Join(t.TempDir(), "seg.mp4")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := MuxWrite(f, seg); err != nil {
		t.Fatalf("mux: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	rf, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer rf.Close()
	got, err := ReadSegment(rf, seg.Frames)
	if err != nil {
		t.Fatalf("reparse: %v", err)
	}
	if err := Verify(seg, got); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if Hash(seg) != HashResult(got) {
		t.Fatal("content digest changed across the round trip")
	}
	// Every track carries one stss keyframe entry at its first sample.
	if len(got.Keyframes) != len(seg.Tracks) {
		t.Fatalf("keyframe index wrong: %v", got.Keyframes)
	}
	for _, k := range got.Keyframes {
		if k != 1 {
			t.Fatalf("keyframe sample number = %d want 1", k)
		}
	}
}

func TestMuxAllBitDepthsAndMono(t *testing.T) {
	for _, bits := range []uint16{16, 24, 32} {
		frames := uint64(37)
		pcm := make([]byte, int(frames)*int(bits)/8)
		for i := range pcm {
			pcm[i] = byte(i + int(bits))
		}
		seg := Segment{
			Frames: clock.FrameRange{Start: 128, End: 128 + frames},
			Tracks: []TrackSpec{{TrackID: 7, BitsPerSample: bits, SampleRate: 44100}},
			PCM:    [][]byte{pcm},
		}
		seeker := newBufSeeker(nil)
		if err := MuxWrite(seeker, seg); err != nil {
			t.Fatalf("mux %d-bit: %v", bits, err)
		}
		got, err := ReadSegment(bytes.NewReader(seeker.bytes()), seg.Frames)
		if err != nil {
			t.Fatalf("reparse %d-bit: %v", bits, err)
		}
		if err := Verify(seg, got); err != nil {
			t.Fatalf("verify %d-bit: %v", bits, err)
		}
	}
}

func TestVerifyDetectsTamperedPayload(t *testing.T) {
	seg := makeSegment(t, 16)
	seeker := newBufSeeker(nil)
	if err := MuxWrite(seeker, seg); err != nil {
		t.Fatal(err)
	}
	raw := seeker.bytes()
	// Flip one byte inside mdat (ftyp/moov precede mdat, but mdat starts
	// early enough here that a mid-file byte is payload).
	tampered := append([]byte(nil), raw...)
	idx := bytes.Index(tampered, []byte("mdat")) + 12
	tampered[idx] ^= 0xFF
	got, err := ReadSegment(bytes.NewReader(tampered), seg.Frames)
	if err != nil {
		// A corrupt index may fail to parse at all — that is also a failure.
		return
	}
	if err := Verify(seg, got); err == nil {
		t.Fatal("tampered payload passed verification")
	}
}

// bufSeeker is an in-memory io.WriteSeeker/io.ReadSeeker supporting the
// in-place seeks go-mp4 performs when finalizing box sizes.
type bufSeeker struct {
	data []byte
	pos  int64
}

func newBufSeeker(_ *bytes.Buffer) *bufSeeker { return &bufSeeker{} }

func (b *bufSeeker) Write(p []byte) (int, error) {
	end := b.pos + int64(len(p))
	if end > int64(len(b.data)) {
		grown := make([]byte, end)
		copy(grown, b.data)
		b.data = grown
	}
	copy(b.data[b.pos:end], p)
	b.pos = end
	return len(p), nil
}

func (b *bufSeeker) Read(p []byte) (int, error) {
	if b.pos >= int64(len(b.data)) {
		return 0, errEOF
	}
	n := copy(p, b.data[b.pos:])
	b.pos += int64(n)
	return n, nil
}

func (b *bufSeeker) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case 0:
		b.pos = offset
	case 1:
		b.pos += offset
	case 2:
		b.pos = int64(len(b.data)) + offset
	}
	return b.pos, nil
}

func (b *bufSeeker) bytes() []byte { return b.data }
