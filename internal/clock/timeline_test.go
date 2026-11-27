package clock

import (
	"testing"

	merr "mediatool/internal/errors"
)

func TestScaleConversions(t *testing.T) {
	cases := []struct {
		ts    uint32
		ticks int64
		ns    int64
	}{
		{48000, 48000, NanosPerSecond},
		{44100, 44100, NanosPerSecond},
		{90000, 3003, 33_366_666}, // 30000/1001 fps frame
		{48000, 24000, 500_000_000},
		{48000, -48000, -NanosPerSecond},
	}
	for _, c := range cases {
		ns, err := ScaleToNanos(c.ts, c.ticks)
		if err != nil {
			t.Fatalf("ScaleToNanos(%d,%d): %v", c.ts, c.ticks, err)
		}
		if ns != c.ns {
			t.Fatalf("ScaleToNanos(%d,%d) = %d, want %d", c.ts, c.ticks, ns, c.ns)
		}
		back, err := NanosToScale(c.ts, ns)
		if err != nil || back != c.ticks {
			t.Fatalf("roundtrip ticks=%d got=%d err=%v", c.ticks, back, err)
		}
	}
	if _, err := ScaleToNanos(0, 1); err == nil {
		t.Fatal("zero timescale must error")
	}
}

func TestTimelineMonotonicAndVFR(t *testing.T) {
	tl := NewTimeline()
	// VFR: 40ms then 20ms frames.
	pts := []int64{0, 40_000_000, 60_000_000, 80_000_000}
	for i, p := range pts {
		if err := tl.Append(Sample{Index: i, PTS: p, DTS: p, Duration: 20_000_000}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	if err := tl.Append(Sample{PTS: 70_000_000, DTS: 70_000_000}); err == nil ||
		merr.AsCode(err) != merr.CodeTimeWentBack {
		t.Fatalf("backwards PTS accepted: %v", err)
	}
	if err := tl.Append(Sample{PTS: 100_000_000, DTS: 110_000_000}); err == nil {
		t.Fatal("DTS after PTS accepted")
	}
	// At() must use actual PTS, not a fixed frame duration.
	if got := tl.At(45_000_000); got != 1 {
		t.Fatalf("At(45ms) = %d, want frame 1", got)
	}
	if got := tl.At(65_000_000); got != 2 {
		t.Fatalf("At(65ms) = %d, want frame 2", got)
	}
	start, end := tl.Range()
	if start != 0 || end != 100_000_000 {
		t.Fatalf("range = %d..%d", start, end)
	}
}

func TestPercentile(t *testing.T) {
	v := []int64{5, 1, 3, 2, 4}
	if got := Percentile(v, 95); got != 5 {
		t.Fatalf("p95 = %d, want 5", got)
	}
	if got := Percentile(v, 50); got != 3 {
		t.Fatalf("p50 = %d, want 3", got)
	}
	if Percentile(nil, 95) != 0 {
		t.Fatal("empty percentile must be zero")
	}
}

func TestUnwrapper32WrapsAndBroken(t *testing.T) {
	u := NewUnwrapper32()
	seq := []uint32{0xFFFFFFF0, 0xFFFFFFF8, 0x00000000, 0x00000010}
	want := []uint64{0xFFFFFFF0, 0xFFFFFFF8, 0x100000000, 0x100000010}
	for i, v := range seq {
		got, err := u.Unwrap(v)
		if err != nil {
			t.Fatalf("wrap %d: %v", i, err)
		}
		if got != want[i] {
			t.Fatalf("unwrap(%#x) = %#x, want %#x", v, got, want[i])
		}
	}
	if u.Wraps() != 1 {
		t.Fatalf("wraps = %d", u.Wraps())
	}
	if _, err := u.Unwrap(0x80000010); err == nil ||
		merr.AsCode(err) != merr.CodeWrapBroken {
		t.Fatalf("non-provable jump accepted: %v", err)
	}
}

func TestUnwrapper16(t *testing.T) {
	u := NewUnwrapper16()
	for _, v := range []uint16{0xFFFE, 0xFFFF, 0x0000, 0x0001} {
		if _, err := u.Unwrap(v); err != nil {
			t.Fatalf("seq %#x: %v", v, err)
		}
	}
	got, _ := u.Unwrap(0x0002)
	if got != 65538 {
		t.Fatalf("unwrapped seq = %d, want 65538", got)
	}
}

func TestDriftEstimator(t *testing.T) {
	d := &DriftEstimator{}
	// Remote runs 100 ppm fast: remote = local * 1.0001.
	for i := int64(0); i < 10; i++ {
		local := i * NanosPerSecond
		d.Add(local, local+local/10_000)
	}
	if rate := d.RatePPM(); rate < 99 || rate > 101 {
		t.Fatalf("drift rate = %.3f ppm, want ~100", rate)
	}
	if err := d.RejectIfExceeds(50); err == nil ||
		merr.AsCode(err) != merr.CodeDriftTooLarge {
		t.Fatalf("drift bound not enforced: %v", err)
	}
	if err := d.RejectIfExceeds(500); err != nil {
		t.Fatalf("drift within bound rejected: %v", err)
	}
	off := d.OffsetNanos(5 * NanosPerSecond)
	if off < 499_000 || off > 501_000 {
		t.Fatalf("offset = %d ns, want ~500000", off)
	}
}
