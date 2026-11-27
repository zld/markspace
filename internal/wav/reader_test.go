package wav

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"mediakernel/internal/clock"
	"mediakernel/internal/fixture"
)

func writeTemp(t *testing.T, name string, buf []byte) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, buf, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestOpenAndStream(t *testing.T) {
	buf, err := fixture.BuildWAV(fixture.WAVOptions{
		Channels: 2, SampleRate: 48000, Frames: 1000, BitsPerSample: 16,
	})
	if err != nil {
		t.Fatal(err)
	}
	p := writeTemp(t, "ok.wav", buf)
	r, err := Open(filepath.Dir(p), filepath.Base(p))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	h := r.Header()
	if h.SampleRate != 48000 || h.Channels != 2 || h.BitsPerSample != 16 {
		t.Fatalf("header = %+v", h)
	}
	if h.Frames() != 1000 || h.FrameSize() != 4 {
		t.Fatalf("geometry frames=%d fs=%d", h.Frames(), h.FrameSize())
	}
	iter, err := r.Blocks(256)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for {
		b, err := iter.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if b.FrameCount <= 0 || len(b.Data) != int(b.FrameCount)*4 {
			t.Fatalf("bad block %+v", b)
		}
		total += b.FrameCount
	}
	if total != 1000 {
		t.Fatalf("streamed %d frames", total)
	}
}

func TestTruncatedWAVReportsOffset(t *testing.T) {
	good, _ := fixture.BuildWAV(fixture.WAVOptions{
		Channels: 2, SampleRate: 48000, Frames: 100, BitsPerSample: 16,
	})
	// Remove 3 bytes: within one sample frame (4 bytes) of the physical end.
	bad, err := fixture.TruncateTail(good, 3)
	if err != nil {
		t.Fatal(err)
	}
	p := writeTemp(t, "trunc.wav", bad)
	r, err := Open(filepath.Dir(p), filepath.Base(p))
	if err != nil {
		// RIFF-level size check may catch it eagerly; offset must be EOF.
		ce, ok := clock.AsError(err)
		if !ok || ce.Category != clock.CatTruncated || ce.ByteOffset != int64(len(bad)) {
			t.Fatalf("eager truncation offset = %v", err)
		}
		return
	}
	defer r.Close()
	iter, err := r.Blocks(64)
	if err != nil {
		t.Fatal(err)
	}
	var saw *clock.Error
	for {
		_, err := iter.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if ce, ok := clock.AsError(err); ok && ce.Category == clock.CatTruncated {
			saw = ce
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if saw == nil {
		t.Fatal("truncated payload must produce a structured error")
	}
	// Reported offset within one sample frame of the physical end.
	if diff := int64(len(bad)) - saw.ByteOffset; diff < 0 || diff > 4 {
		t.Fatalf("offset %d not within one frame of EOF %d", saw.ByteOffset, len(bad))
	}
}

func TestIllegalDataLength(t *testing.T) {
	good, _ := fixture.BuildWAV(fixture.WAVOptions{
		Channels: 1, SampleRate: 8000, Frames: 10, BitsPerSample: 16,
	})
	bad := fixture.MutateDataLength(good, 0x7FFFFFFF)
	p := writeTemp(t, "badlen.wav", bad)
	if _, err := Open(filepath.Dir(p), filepath.Base(p)); err == nil {
		t.Fatal("out-of-bounds data length must fail")
	} else if ce, ok := clock.AsError(err); !ok ||
		(ce.Category != clock.CatBadLength && ce.Category != clock.CatTruncated) ||
		!ce.HasOffset {
		t.Fatalf("want located bad-length error, got %v", err)
	}
}

func TestMisalignedAndUnsupported(t *testing.T) {
	good, _ := fixture.BuildWAV(fixture.WAVOptions{
		Channels: 2, SampleRate: 48000, Frames: 10, BitsPerSample: 16,
	})
	// Declare an odd data length that cannot form whole 4-byte frames.
	bad := fixture.MutateDataLength(good, 39)
	p := writeTemp(t, "mis.wav", bad)
	_, err := Open(filepath.Dir(p), filepath.Base(p))
	if ce, ok := clock.AsError(err); !ok || ce.Category != clock.CatMisaligned {
		t.Fatalf("want misaligned, got %v", err)
	}

	// Flip the format tag away from PCM.
	nonPCM := append([]byte(nil), good...)
	binary.LittleEndian.PutUint16(nonPCM[20:22], 3) // IEEE float
	p2 := writeTemp(t, "float.wav", nonPCM)
	if _, err := Open(filepath.Dir(p2), filepath.Base(p2)); err == nil {
		t.Fatal("non-PCM encoding must be rejected")
	} else if ce, ok := clock.AsError(err); !ok || ce.Category != clock.CatUnsupported {
		t.Fatalf("want unsupported_encoding, got %v", err)
	}
}

func TestReadFrameRangeBoundaries(t *testing.T) {
	buf, _ := fixture.BuildWAV(fixture.WAVOptions{
		Channels: 1, SampleRate: 8000, Frames: 50, BitsPerSample: 16,
	})
	p := writeTemp(t, "rng.wav", buf)
	r, err := Open(filepath.Dir(p), filepath.Base(p))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	out, err := r.ReadFrameRange(FrameRange{Start: 10, End: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 20 {
		t.Fatalf("range bytes = %d", len(out))
	}
	// Half-open semantics: adjacent ranges must not overlap and stay in order.
	a, _ := r.ReadFrameRange(FrameRange{Start: 0, End: 10})
	b, _ := r.ReadFrameRange(FrameRange{Start: 10, End: 20})
	if len(a) != 20 || len(b) != 20 {
		t.Fatal("half-open range sizes wrong")
	}
	if _, err := r.ReadFrameRange(FrameRange{Start: 20, End: 20}); err == nil {
		t.Fatal("empty range must fail")
	}
	if _, err := r.ReadFrameRange(FrameRange{Start: 0, End: 51}); err == nil {
		t.Fatal("range past end must fail")
	}
}
