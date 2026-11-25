package wav

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"mediad/internal/fault"
)

func makePCM(t *testing.T, f Format, frames int) []byte {
	t.Helper()
	pcm := make([]byte, frames*f.FrameSize())
	for i := range pcm {
		pcm[i] = byte(i*7 + 3)
	}
	return pcm
}

func TestRoundTripAllBitDepthsAndChannels(t *testing.T) {
	for _, bits := range []uint16{16, 24, 32} {
		for _, ch := range []uint16{1, 2, 3, 6, 8} {
			f := Format{AudioFormat: 1, NumChannels: ch, SampleRate: 48000, BitsPerSample: bits}
			pcm := makePCM(t, f, 100)
			raw, err := Encode(f, pcm)
			if err != nil {
				t.Fatalf("encode %d-bit/%dch: %v", bits, ch, err)
			}
			got, gf, err := ReadAllPCM(NewDecoder(bytes.NewReader(raw)))
			if err != nil {
				t.Fatalf("decode %d-bit/%dch: %v", bits, ch, err)
			}
			if gf != f || !bytes.Equal(got, pcm) {
				t.Fatalf("roundtrip mismatch %d-bit/%dch", bits, ch)
			}
		}
	}
}

// feedInChunks replays a WAV stream through the decoder in arbitrarily small
// pieces, exactly how network arrivals behave.
func feedInChunks(t *testing.T, raw []byte, chunkSize int) []byte {
	t.Helper()
	d := NewDecoder(&chunkReader{data: raw, size: chunkSize})
	out, _, err := ReadAllPCM(d)
	if err != nil {
		t.Fatalf("streaming decode (chunk=%d): %v", chunkSize, err)
	}
	return out
}

type chunkReader struct {
	data []byte
	size int
	pos  int
}

func (c *chunkReader) Read(p []byte) (int, error) {
	if c.pos >= len(c.data) {
		return 0, io.EOF
	}
	n := len(p)
	if n > c.size {
		n = c.size
	}
	if rem := len(c.data) - c.pos; n > rem {
		n = rem
	}
	n = copy(p, c.data[c.pos:c.pos+n])
	c.pos += n
	return n, nil
}

func TestChunkedFeeding(t *testing.T) {
	f := Format{AudioFormat: 1, NumChannels: 2, SampleRate: 44100, BitsPerSample: 24}
	pcm := makePCM(t, f, 257)
	raw, err := Encode(f, pcm)
	if err != nil {
		t.Fatal(err)
	}
	for _, size := range []int{1, 3, 7, 16, 31, 128, 4096} {
		got := feedInChunks(t, raw, size)
		if !bytes.Equal(got, pcm) {
			t.Fatalf("payload mismatch with chunk size %d", size)
		}
	}
}

func TestTruncatedChunkReportsOffset(t *testing.T) {
	f := Format{AudioFormat: 1, NumChannels: 2, SampleRate: 48000, BitsPerSample: 16}
	raw, err := Encode(f, makePCM(t, f, 16))
	if err != nil {
		t.Fatal(err)
	}
	cut := len(raw) - 3 // lose the final 1.5 frames
	d := NewDecoder(bytes.NewReader(raw[:cut]))
	_, _, err = ReadAllPCM(d)
	fe, ok := fault.As(err)
	if !ok || fe.Category != fault.CatTruncated {
		t.Fatalf("expected truncated fault, got %v", err)
	}
	if fe.Offset != int64(cut) {
		t.Fatalf("offset = %d want %d", fe.Offset, cut)
	}
}

func TestNonIntegralFrameRejected(t *testing.T) {
	f := Format{AudioFormat: 1, NumChannels: 2, SampleRate: 48000, BitsPerSample: 16}
	raw, _ := Encode(f, makePCM(t, f, 4))
	// Patch the data chunk size to an odd number of bytes so the declared
	// payload cannot contain whole frames, and trim the file to that size.
	raw[len(raw)-4] = 5 // low byte of little-endian data size: 16 -> 5
	raw = raw[:len(raw)-11]
	d := NewDecoder(bytes.NewReader(raw))
	for {
		_, err := d.ReadFrame()
		if errors.Is(err, ErrNeedsData) || err == io.EOF {
			break
		}
		if fe, ok := fault.As(err); ok {
			if fe.Category != fault.CatAlignment && fe.Category != fault.CatTruncated {
				t.Fatalf("unexpected category %s", fe.Category)
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	// At end of input the soft "need data" state must resolve to a fault.
	err := d.Finalize()
	fe, ok := fault.As(err)
	if !ok || (fe.Category != fault.CatAlignment && fe.Category != fault.CatTruncated) {
		t.Fatalf("expected alignment/truncation fault, got %v", err)
	}
}

func TestContradictoryHeadersRejected(t *testing.T) {
	f := Format{AudioFormat: 1, NumChannels: 2, SampleRate: 48000, BitsPerSample: 16}
	raw, _ := Encode(f, makePCM(t, f, 4))

	cases := map[string]func([]byte) []byte{
		"not riff": func(b []byte) []byte { b[0] = 'X'; return b },
		"bad tag": func(b []byte) []byte {
			b[20] = 3 // audio format field at offset 20
			return b
		},
		"bad bits": func(b []byte) []byte {
			b[34] = 8 // bits per sample low byte: 16 -> 8
			return b
		},
		"block align": func(b []byte) []byte {
			b[32] = 1 // block align low byte: 4 -> 1
			return b
		},
		"channel count": func(b []byte) []byte {
			b[22] = 9 // num channels low byte: 2 -> 9
			return b
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cp := append([]byte(nil), raw...)
			cp = mutate(cp)
			_, err := NewDecoder(bytes.NewReader(cp)).ReadFrame()
			fe, ok := fault.As(err)
			if !ok || fe.Category != fault.CatHeader {
				t.Fatalf("expected header fault, got %v", err)
			}
		})
	}
}
