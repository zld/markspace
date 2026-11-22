package timeline

import (
	"math"
	"testing"

	"github.com/pion/rtp"
)

func TestUnwrap32BitWraparound(t *testing.T) {
	c, err := NewClock(TrackConfig{TrackNumber: 1, Kind: KindVideo, TicksPerSecond: 1000, WrapModulo: 1 << 32})
	if err != nil {
		t.Fatal(err)
	}
	// 1 ms ticks: jump from near the top of the 32-bit range across the wrap.
	raw := []uint64{0xFFFFFFF0, 0xFFFFFFF8, 0x00000002, 0x0000000A}
	var ticks []int64
	var frames []Frame
	for i, r := range raw {
		f, err := c.Normalise(r, int64(i*10))
		if err != nil {
			t.Fatalf("sample %d: %v", i, err)
		}
		ticks = append(ticks, f.Ticks)
		frames = append(frames, f)
	}
	want := []int64{0xFFFFFFF0, 0xFFFFFFF8, 0x100000002, 0x10000000A}
	for i := range want {
		if ticks[i] != want[i] {
			t.Fatalf("ticks[%d]=%d want %d", i, ticks[i], want[i])
		}
		if frames[i].T0 != ticks[i]*1_000_000 { // 1 tick = 1 ms
			t.Fatalf("t0[%d]=%d", i, frames[i].T0)
		}
	}
}

func TestAmbiguousTimestampFailsClosed(t *testing.T) {
	mod := uint64(1 << 32)
	c, _ := NewClock(TrackConfig{TrackNumber: 2, TicksPerSecond: 90000, WrapModulo: mod})
	if _, err := c.Normalise(0, 0); err != nil {
		t.Fatal(err)
	}
	// A jump of exactly half the period cannot be oriented uniquely.
	_, err := c.Normalise(mod/2, 8)
	var amb *AmbiguousError
	if err == nil {
		t.Fatal("want ambiguity error, got nil")
	}
	if e, ok := err.(*AmbiguousError); !ok || e.Offset != 8 || e.TrackNumber != 2 {
		t.Fatalf("want *AmbiguousError with offset=8, got %T %v", err, err)
	}
	_ = amb

	// Backwards step on a non-wrapping clock is also ambiguous.
	c2, _ := NewClock(TrackConfig{TrackNumber: 3, TicksPerSecond: 1000})
	_, _ = c2.Normalise(100, 0)
	if _, err := c2.Normalise(50, 4); err == nil {
		t.Fatal("backwards non-wrap timecode must fail")
	}
}

func TestVFRSpacingPreserved(t *testing.T) {
	c, _ := NewClock(TrackConfig{TrackNumber: 1, TicksPerSecond: 1000})
	raw := []uint64{0, 33, 67, 133, 167} // irregular 33/34/66 ms gaps
	var frames []Frame
	for i, r := range raw {
		f, err := c.Normalise(r, int64(i))
		if err != nil {
			t.Fatal(err)
		}
		frames = append(frames, f)
	}
	rep, err := NewDriftEstimator(1, 500).Fit() // identity (no drift)
	if err != nil {
		t.Fatal(err)
	}
	out, err := CorrectFrames(frames, rep)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(raw); i++ {
		if got := out[i].T0 - out[i-1].T0; got != int64(raw[i]-raw[i-1])*1_000_000 {
			t.Fatalf("VFR gap %d altered: %d ns", i, got)
		}
	}
}

func TestDriftEstimateAndThreshold(t *testing.T) {
	// Reference clock runs 100 ppm faster than the track: ref = 1.0001*t.
	est := NewDriftEstimator(7, 50)
	for ms := int64(0); ms <= 10000; ms += 250 {
		track := ms * 1_000_000
		ref := int64(math.Round(1.0001 * float64(track)))
		est.Add(track, ref)
	}
	rep, err := est.Fit()
	if err == nil {
		t.Fatalf("want drift failure, got report %+v", rep)
	}
	de, ok := err.(*DriftError)
	if !ok || math.Abs(de.Report.PPM-100) > 1 {
		t.Fatalf("want ~100 ppm DriftError, got %v", err)
	}

	// Same slope inside tolerance passes and correction removes the offset.
	ok2 := NewDriftEstimator(7, 500)
	for ms := int64(0); ms <= 10000; ms += 250 {
		track := ms * 1_000_000
		ok2.Add(track, int64(math.Round(1.0001*float64(track))))
	}
	rep2, err := ok2.Fit()
	if err != nil {
		t.Fatalf("drift within threshold must pass: %v", err)
	}
	cor := NewCorrector(rep2)
	if got := cor.Apply(10_000_000_000); math.Abs(float64(got-10_001_000_000)) > 1 {
		t.Fatalf("corrected t0=%d", got)
	}
}

func TestCrossTrackMonotonic(t *testing.T) {
	video := []Frame{
		{TrackNumber: 1, T0: 0}, {TrackNumber: 1, T0: 33_000_000},
		{TrackNumber: 1, T0: 67_000_000},
	}
	audio := []Frame{
		{TrackNumber: 2, T0: 10_000_000}, {TrackNumber: 2, T0: 33_000_000},
		{TrackNumber: 2, T0: 60_000_000},
	}
	if err := AssertCrossTrackMonotonic(map[uint64][]Frame{1: video, 2: audio}); err != nil {
		t.Fatalf("interleaved monotonic stream rejected: %v", err)
	}

	bad := []Frame{
		{TrackNumber: 3, T0: 30_000_000, ByteOffset: 70},
		{TrackNumber: 3, T0: 25_000_000, ByteOffset: 77}, // backwards inside the stream
	}
	if err := AssertCrossTrackMonotonic(map[uint64][]Frame{1: video, 3: bad}); err == nil {
		t.Fatal("cross-track regression must be detected")
	}
}

func TestRTPSequenceBoundaries(t *testing.T) {
	var cur SeqCursor
	seqs := []uint16{0xFFFE, 0xFFFF, 0x0000, 0x0001, 0x0003}
	wantDist := []int{1, 1, 1, 1, 2}
	var sawGap bool
	for i, s := range seqs {
		p := &rtp.Packet{}
		p.SequenceNumber = s
		d, err := cur.Observe(p)
		if d != wantDist[i] {
			t.Fatalf("seq %d distance=%d want %d", s, d, wantDist[i])
		}
		if i == len(seqs)-1 {
			if err == nil {
				t.Fatal("gap must be reported")
			}
			sawGap = true
		} else if err != nil {
			t.Fatalf("unexpected error at %d: %v", s, err)
		}
	}
	if !sawGap || cur.Cycles() != 1 {
		t.Fatalf("gap=%v cycles=%d", sawGap, cur.Cycles())
	}

	// Duplicate packet: distance 0, no cycle counted.
	var dup SeqCursor
	p := &rtp.Packet{}
	p.SequenceNumber = 5
	_, _ = dup.Observe(p)
	d, _ := dup.Observe(p)
	if d != 0 {
		t.Fatalf("duplicate distance=%d want 0", d)
	}
}
