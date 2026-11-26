package media

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"continuity/internal/clock"
)

func TestCMAFRoundTrip(t *testing.T) {
	w := &CMAFWriter{Sequence: 1, Timescale: 48000, TrackID: 1}
	payload := make([]byte, 64)
	for i := range payload {
		payload[i] = uint8(i)
	}
	frag, err := w.Fragment(96000, payload) // 2.0 s at 48 kHz
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "seg.m4s")
	if err := os.WriteFile(path, frag, 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := InspectSegment(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasSTyp || !info.HasMoof || !info.HasMdat {
		t.Fatalf("missing boxes: %s", info)
	}
	if info.BaseMediaDecode != 0 {
		t.Fatalf("bmdt=%d", info.BaseMediaDecode)
	}
	if info.SampleCount != 1 || len(info.SampleDeltas) != 1 || info.SampleDeltas[0] != 96000 {
		t.Fatalf("trun samples=%v deltas=%v", info.SampleCount, info.SampleDeltas)
	}
	dur, err := info.DurationRat(48000)
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := dur.Cmp(clock.MustR(2, 1)); c != 0 {
		t.Fatalf("duration = %s", dur)
	}
	if string(info.MdatPayload) != string(payload) {
		t.Fatalf("mdat payload mismatch (%d bytes)", len(info.MdatPayload))
	}
}

func TestTruncatedMdat(t *testing.T) {
	w := &CMAFWriter{Sequence: 1, Timescale: 1000, TrackID: 1}
	frag, err := w.Fragment(2000, []byte{1, 2, 3, 4})
	if err != nil {
		t.Fatal(err)
	}
	// Chop the tail so the mdat declared length exceeds available bytes.
	truncated := frag[:len(frag)-3]
	path := filepath.Join(t.TempDir(), "bad.m4s")
	if err := os.WriteFile(path, truncated, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = InspectSegment(path)
	me := clock.AsMedia(err)
	if me == nil || me.Kind != clock.KindCorruptMedia || me.Offset < 0 {
		t.Fatalf("want offset-located corrupt media error, got %+v", me)
	}
}

func TestTruncatedHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "short.m4s")
	if err := os.WriteFile(path, []byte{0, 1, 2}, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectSegment(path); clock.KindOf(err) != clock.KindCorruptMedia {
		t.Fatalf("want corrupt media, got %v", err)
	}
}

func TestBitrateDeviation(t *testing.T) {
	// 10000 bytes over 1s -> 80000 bps.
	rep, err := Bitrate(10000, clock.FromInt(1))
	if err != nil {
		t.Fatal(err)
	}
	rep = rep.WithTarget(80000)
	if rep.ActualBPS != 80000 {
		t.Fatalf("actual = %d", rep.ActualBPS)
	}
	if rep.DeviationPct != 0 {
		t.Fatalf("deviation = %f", rep.DeviationPct)
	}
	// 5% over target.
	over, err := Bitrate(10500, clock.FromInt(1))
	if err != nil {
		t.Fatal(err)
	}
	over = over.WithTarget(80000)
	if math.Abs(over.DeviationPct-5.0) > 1e-9 {
		t.Fatalf("deviation = %f", over.DeviationPct)
	}
}

func TestPSNRIdenticalAndNoisy(t *testing.T) {
	ref := GradientFrame(16, 16)
	if ps, err := PSNR(ref, ref); err != nil || !math.IsInf(ps, 1) {
		t.Fatalf("identical PSNR = %v err=%v", ps, err)
	}
	noisy := NoisyFrame(ref, 4, 42)
	ps, err := PSNR(ref, noisy)
	if err != nil {
		t.Fatal(err)
	}
	if !(ps > 30 && ps < 60) {
		t.Fatalf("unexpected PSNR %f", ps)
	}
	// Determinism: same seed reproduces identical frame/PSNR.
	again := NoisyFrame(ref, 4, 42)
	ps2, _ := PSNR(ref, again)
	if ps != ps2 {
		t.Fatalf("non-deterministic PSNR %f vs %f", ps, ps2)
	}
}

func TestPSNRDimensionMismatch(t *testing.T) {
	a := NewGrayFrame(2, 2)
	b := NewGrayFrame(3, 2)
	if _, err := PSNR(a, b); clock.KindOf(err) != clock.KindInvalidInput {
		t.Fatalf("want invalid input, got %v", err)
	}
}

func TestFramePayloadRoundTrip(t *testing.T) {
	ref := GradientFrame(8, 4)
	frame, err := FrameFromPayload(ref.Pix, 8, 4)
	if err != nil {
		t.Fatal(err)
	}
	if ps, _ := PSNR(ref, frame); !math.IsInf(ps, 1) {
		t.Fatalf("payload round trip PSNR = %v", ps)
	}
	if _, err := FrameFromPayload([]byte{1, 2}, 8, 4); clock.KindOf(err) != clock.KindInvalidInput {
		t.Fatalf("want size mismatch error")
	}
}

func TestPadToTargetSize(t *testing.T) {
	w := &CMAFWriter{Timescale: 1000}
	frag, err := w.Fragment(2000, make([]byte, 100))
	if err != nil {
		t.Fatal(err)
	}
	padded := PadToTargetSize(frag, 5000)
	if len(padded) != 5000 {
		t.Fatalf("padded size = %d want 5000", len(padded))
	}
	// Padded fragment must still pass inspection with intact payload.
	path := filepath.Join(t.TempDir(), "padded.m4s")
	if err := os.WriteFile(path, padded, 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := InspectSegment(path)
	if err != nil {
		t.Fatal(err)
	}
	// mdat payload grew by exactly the pad amount.
	wantPayload := 100 + (5000 - len(frag))
	if len(info.MdatPayload) != wantPayload {
		t.Fatalf("mdat payload = %d want %d", len(info.MdatPayload), wantPayload)
	}
}
