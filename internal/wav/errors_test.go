package wav

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"testing"
)

func TestTruncatedChunkIsRejected(t *testing.T) {
	data := int16Data(1, 2, 3, 4)
	raw := buildWAV(fmtChunk(FormatPCM, 1, 48000, 16), testChunk{"data", data})
	// Claim two more bytes than the file holds.
	binary.LittleEndian.PutUint32(raw[4:8], uint32(len(raw)-4+2))
	path := writeTemp(t, raw[:len(raw)-2])
	_, err := Open(path)
	if !errors.Is(err, ErrTruncated) {
		t.Fatalf("err = %v, want ErrTruncated", err)
	}
}

func TestTruncatedSampleDataIsRejected(t *testing.T) {
	data := int16Data(1, 2, 3, 4)
	raw := buildWAV(fmtChunk(FormatPCM, 1, 48000, 16), testChunk{"data", data})
	// Shrink the file without touching the declared chunk sizes.
	path := writeTemp(t, raw[:len(raw)-3])
	_, err := Open(path)
	if err == nil {
		t.Fatal("expected an error for a file shorter than its data chunk")
	}
}

func TestFormatConflictIsRejected(t *testing.T) {
	cases := map[string]testChunk{
		"8-bit pcm":       fmtChunk(FormatPCM, 1, 48000, 8),
		"16-bit float":    fmtChunk(FormatFloat, 1, 48000, 16),
		"alaw":            fmtChunk(6, 1, 48000, 8),
		"bad sample rate": fmtChunk(FormatPCM, 1, 1000, 16),
	}
	for name, chunk := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeTemp(t, buildWAV(chunk, testChunk{"data", make([]byte, 16)}))
			if _, err := Open(path); err == nil {
				t.Fatal("expected an error for an unsupported format")
			}
		})
	}
}

func TestBlockAlignConflictIsRejected(t *testing.T) {
	fc := fmtChunk(FormatPCM, 2, 48000, 16)
	binary.LittleEndian.PutUint16(fc.payload[12:], 3) // should be 4
	path := writeTemp(t, buildWAV(fc, testChunk{"data", make([]byte, 16)}))
	if _, err := Open(path); err == nil {
		t.Fatal("expected an error for inconsistent block alignment")
	}
}

func TestNonFiniteFloatIsRejected(t *testing.T) {
	for name, bits := range map[string]uint32{
		"nan": math.Float32bits(float32(math.NaN())),
		"inf": math.Float32bits(float32(math.Inf(1))),
	} {
		t.Run(name, func(t *testing.T) {
			p := make([]byte, 8)
			binary.LittleEndian.PutUint32(p[0:], math.Float32bits(0.5))
			binary.LittleEndian.PutUint32(p[4:], bits)
			path := writeTemp(t, buildWAV(fmtChunk(FormatFloat, 1, 48000, 32), testChunk{"data", p}))
			r, err := Open(path)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			defer r.Close()
			buf := make([]float32, 2)
			if _, err := r.ReadFrames(buf); !errors.Is(err, ErrNonFinite) {
				t.Fatalf("err = %v, want ErrNonFinite", err)
			}
		})
	}
}

func TestIncrementalReadsCoverAllFrames(t *testing.T) {
	// Feed the reader blocks far smaller than its internal buffer and verify
	// the concatenation equals a single full read.
	n := 1000
	data := make([]byte, 0, n*4)
	for i := 0; i < n; i++ {
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], uint32(int32(i)))
		data = append(data, b[:]...)
	}
	path := writeTemp(t, buildWAV(fmtChunk(FormatPCM, 1, 48000, 32), testChunk{"data", data}))
	r, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r.Close()
	var got []float32
	buf := make([]float32, 37)
	for {
		c, err := r.ReadFrames(buf)
		got = append(got, buf[:c]...)
		if err != nil {
			break
		}
	}
	if len(got) != n {
		t.Fatalf("read %d frames, want %d", len(got), n)
	}
	for i := range got {
		if math.Abs(float64(got[i])-float64(i)/2147483648.0) > 1e-12 {
			t.Fatalf("frame %d = %v", i, got[i])
		}
	}
}

func TestNotRIFFIsRejected(t *testing.T) {
	path := writeTemp(t, []byte("This is not a WAV file at all........"))
	if _, err := Open(path); err == nil {
		t.Fatal("expected an error for non-RIFF input")
	}
	if !bytes.HasPrefix([]byte("RIFF"), []byte("RIFF")) {
		t.Fatal("unreachable")
	}
}
