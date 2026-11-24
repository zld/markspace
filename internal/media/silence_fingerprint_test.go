package media_test

import (
	"testing"
	"time"

	"medpipe/internal/media"
)

func frameAt(i int, energy int, content byte) media.Frame {
	return media.Frame{
		Sequence: uint16(i), PTS: time.Duration(i) * 20 * time.Millisecond,
		Duration: 20 * time.Millisecond, Energy: energy, Content: []byte{content, byte(i)},
	}
}

func TestShortLowEnergyRunIsSuppressedAndDoesNotChangeBoundary(t *testing.T) {
	det := media.NewSilenceDetector(media.SilenceConfig{EnergyThreshold: 100, MinDuration: 500 * time.Millisecond})
	var ranges []media.SilenceRange
	for i := 0; i < 100; i++ {
		energy := 500
		if i >= 40 && i < 50 { // 10 frames = 200ms, below 500ms
			energy = 10
		}
		if r := det.Add(frameAt(i, energy, byte(i))); r != nil {
			ranges = append(ranges, *r)
		}
	}
	if r := det.Flush(); r != nil {
		ranges = append(ranges, *r)
	}
	if len(ranges) != 0 {
		t.Fatalf("short run produced range/cut: %+v", ranges)
	}
	if det.ShortRuns() != 1 {
		t.Fatalf("suppressed runs=%d want 1", det.ShortRuns())
	}

	seg := media.NewSegmenter(media.SegmentConfig{Duration: time.Second, MaxDuration: time.Second})
	var cuts int
	for i := 0; i < 100; i++ {
		energy := 500
		if i >= 40 && i < 50 {
			energy = 10
		}
		built, err := seg.Accept(frameAt(i, energy, byte(i)))
		if err != nil {
			t.Fatal(err)
		}
		if built != nil {
			cuts++
		}
	}
	if built := seg.Flush(); built != nil {
		cuts++
	}
	// 100 frames * 20ms = 2s: one forced boundary after frame 50 plus flush.
	if cuts != 2 {
		t.Fatalf("cuts=%d want 2; short silence changed segmentation", cuts)
	}
}

func TestFingerprintMismatchDoesNotMerge(t *testing.T) {
	f1 := frameAt(0, 1000, 'a')
	f1.Hash = media.HashFrame(f1)
	f2 := frameAt(1, 2000, 'b')
	f2.Hash = media.HashFrame(f2)

	a := media.NewFingerprint()
	a.Add(f1)
	a.Add(f2)
	b := media.NewFingerprint()
	b.Add(f1)
	b.Add(f2)
	if a.Sum() != b.Sum() {
		t.Fatal("identical frame sequences have different fingerprints")
	}

	changed := f2
	changed.Content = []byte("different")
	changed.Hash = media.HashFrame(changed)
	c := media.NewFingerprint()
	c.Add(f1)
	c.Add(changed)
	if a.Sum() == c.Sum() {
		t.Fatal("mutated content unexpectedly matched fingerprint")
	}

	d := media.NewDeduper()
	if got := d.Observe(a.Sum(), 1); got != -1 {
		t.Fatalf("first observation duplicateOf=%d want -1", got)
	}
	if got := d.Observe(b.Sum(), 9); got != 1 {
		t.Fatalf("identical fingerprint duplicateOf=%d want 1", got)
	}
	if got := d.Observe(c.Sum(), 10); got != -1 {
		t.Fatalf("different fingerprint merged as %d", got)
	}
}
