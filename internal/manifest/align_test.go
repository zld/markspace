package manifest

import (
	"bytes"
	"testing"

	"mediadag/internal/inspect"
)

func frames(n int, fps float64, bytesPerFrame int, idrEvery int) []inspect.Frame {
	out := make([]inspect.Frame, n)
	for i := range out {
		out[i] = inspect.Frame{
			TimeMs: float64(i) * 1000 / fps,
			Bytes:  bytesPerFrame,
			IDR:    idrEvery > 0 && i%idrEvery == 0,
		}
	}
	return out
}

func TestBuildAligned(t *testing.T) {
	an := &inspect.Analysis{
		FrameList:  frames(180, 30, 7500, 60), // 6 s @30fps, IDR every 2 s
		MediaBytes: 180 * 7500,
	}
	rep, err := Build(an, 2000, 1800, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Segments) != 3 {
		t.Fatalf("segments = %d, want 3", len(rep.Segments))
	}
	if rep.AlignmentErrMs > 5 {
		t.Fatalf("alignment err = %v ms", rep.AlignmentErrMs)
	}
	for i, s := range rep.Segments {
		if !s.HasIDR {
			t.Fatalf("segment %d missing IDR", i)
		}
		if s.Frames != 60 {
			t.Fatalf("segment %d frames = %d, want 60", i, s.Frames)
		}
	}
	// 180*7500 bytes over 6 s = 1800 kbps
	if d := rep.BitrateDeviationPct(); d > 0.1 {
		t.Fatalf("bitrate deviation = %v%%", d)
	}
}

func TestAlignmentErrorMeasured(t *testing.T) {
	fl := frames(90, 30, 1000, 30)
	// shift all frames 12 ms off the grid
	for i := range fl {
		fl[i].TimeMs += 12
	}
	an := &inspect.Analysis{FrameList: fl, MediaBytes: 90 * 1000}
	rep, err := Build(an, 1000, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if rep.AlignmentErrMs < 11.9 || rep.AlignmentErrMs > 12.1 {
		t.Fatalf("alignment err = %v, want ~12", rep.AlignmentErrMs)
	}
}

func TestEmptySegmentFromLoss(t *testing.T) {
	fl := frames(90, 30, 1000, 30)
	// drop every frame of the middle second
	var kept []inspect.Frame
	for _, f := range fl {
		if f.TimeMs < 1000 || f.TimeMs >= 2000 {
			kept = append(kept, f)
		}
	}
	an := &inspect.Analysis{FrameList: kept, MediaBytes: len(kept) * 1000}
	rep, err := Build(an, 1000, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Segments) != 3 {
		t.Fatalf("segments = %d", len(rep.Segments))
	}
	if rep.Segments[1].Frames != 0 {
		t.Fatalf("middle segment frames = %d, want 0", rep.Segments[1].Frames)
	}
}

func TestBlockedWhenTimeUncertain(t *testing.T) {
	an := &inspect.Analysis{TimeUncertain: true, FrameList: frames(10, 30, 100, 5)}
	if _, err := Build(an, 1000, 100, 0); err != ErrTimeUncertain {
		t.Fatalf("err = %v, want ErrTimeUncertain", err)
	}
}

// TestDeviationCauseLossVsSender separates transport loss from a genuine
// encoder bitrate change using the loss-compensated sender estimate.
func TestDeviationCauseLossVsSender(t *testing.T) {
	an := &inspect.Analysis{
		FrameList:  frames(180, 30, 6000, 60), // 1440 kbps delivered
		MediaBytes: 180 * 6000,
	}
	// 20% of packets lost: delivered 1440, sender ~1800 -> packet-loss
	rep, err := Build(an, 2000, 1800, 0.20)
	if err != nil {
		t.Fatal(err)
	}
	if d := rep.BitrateDeviationPct(); d < 19 || d > 21 {
		t.Fatalf("actual deviation = %.2f%%, want ~20", d)
	}
	if d := rep.SentDeviationPct(); d > 1 {
		t.Fatalf("sent deviation = %.2f%%, want ~0", d)
	}
	if got := rep.DeviationCause(); got != "packet-loss" {
		t.Fatalf("cause = %q, want packet-loss", got)
	}

	// no loss, sender genuinely low: 1200 vs 1800 target -> sender-change
	an2 := &inspect.Analysis{FrameList: frames(180, 30, 5000, 60), MediaBytes: 180 * 5000}
	rep2, err := Build(an2, 2000, 1800, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := rep2.DeviationCause(); got != "sender-change" {
		t.Fatalf("cause = %q, want sender-change (%.1f%%)", got, rep2.BitrateDeviationPct())
	}

	// loss present but sender also low: still sender-change
	rep3, err := Build(an2, 2000, 1800, 0.1)
	if err != nil {
		t.Fatal(err)
	}
	if got := rep3.DeviationCause(); got != "sender-change" {
		t.Fatalf("cause = %q, want sender-change", got)
	}
}

func TestWriteInitSegment(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteInitSegment(&buf); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	if len(b) < 8 || string(b[4:8]) != "ftyp" {
		t.Fatalf("not an ftyp box: %v", b[:8])
	}
}
