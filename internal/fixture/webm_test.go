package fixture

import (
	"bytes"
	"testing"

	"mediapack/internal/ebml"
	"mediapack/internal/fault"
)

func defaultSpec() Spec {
	return Spec{
		Seed: 7, Codecs: []string{"V_VP9", "A_OPUS", "S_TEXT/WEBVTT"},
		Clusters: 4, FramesPerTrack: 2, PayloadMin: 20, PayloadMax: 60,
	}
}

func TestBuildValidSampleParses(t *testing.T) {
	raw, d := Build(defaultSpec(), CorruptNone)
	if d.CorruptOffset != -1 || d.BlockCount != 4*3*2 {
		t.Fatalf("unexpected detail: %+v", d)
	}
	seg, err := ebml.OpenSegment(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	clusters := 0
	blocks := 0
	err = seg.ScanClusters(func(c *ebml.Cluster) error {
		clusters++
		blocks += len(c.Blocks)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if clusters != 4 || blocks != d.BlockCount {
		t.Fatalf("clusters=%d blocks=%d want 4/%d", clusters, blocks, d.BlockCount)
	}
	// Every recorded interval must point at payload beginning with that
	// codec's marker byte.
	for _, iv := range d.Intervals {
		if iv.End <= iv.Start || iv.Start < 0 || iv.End > int64(len(raw)) {
			t.Fatalf("illegal interval %+v in %d-byte stream", iv, len(raw))
		}
		if raw[iv.Start] != codecMarker(d.Codecs[iv.Track-1]) {
			t.Fatalf("block marker mismatch for track %d at %d", iv.Track, iv.Start)
		}
	}
}

func TestDeterministicBytes(t *testing.T) {
	a, da := Build(defaultSpec(), CorruptNone)
	b, db := Build(defaultSpec(), CorruptNone)
	if !bytes.Equal(a, b) {
		t.Fatal("same seed produced different bytes")
	}
	if da.BlockCount != db.BlockCount {
		t.Fatal("block counts differ across identical builds")
	}
}

func TestUnknownLengthSample(t *testing.T) {
	spec := defaultSpec()
	spec.UnknownLength = true
	raw, d := Build(spec, CorruptNone)
	seg, err := ebml.OpenSegment(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if seg.Element().Size != ebml.UnknownSize {
		t.Fatal("segment should have unknown length")
	}
	err = seg.ScanClusters(func(c *ebml.Cluster) error { return nil })
	if err != nil {
		t.Fatalf("unknown-length stream must parse: %v", err)
	}
	if d.BlockCount != 24 {
		t.Fatalf("block count=%d want 24", d.BlockCount)
	}
}

func TestTruncationOffsetLocalization(t *testing.T) {
	raw, d := Build(defaultSpec(), CorruptTruncate)
	if d.CorruptKind != CorruptTruncate || int64(len(raw)) != d.CorruptOffset {
		t.Fatalf("truncation detail wrong: %+v len=%d", d, len(raw))
	}
	_, err := ScanIntervals(raw)
	fe := fault.Of(err)
	if fe == nil || fe.Kind != fault.KindTruncated || !fe.Recoverable {
		t.Fatalf("expected recoverable truncation, got %v", err)
	}
	if delta := fe.Offset - d.CorruptOffset; delta < -1 || delta > 1 {
		t.Fatalf("fault offset %d not within 1 byte of cut %d", fe.Offset, d.CorruptOffset)
	}
}

func TestBlockOversizeLocalization(t *testing.T) {
	raw, d := Build(defaultSpec(), CorruptBlockOversize)
	_, err := ScanIntervals(raw)
	fe := fault.Of(err)
	if fe == nil || fe.Kind != fault.KindLengthOutOfRange {
		t.Fatalf("expected length-out-of-range, got %v", err)
	}
	// The fault must point inside the illegal block's structural span:
	// from its size VINT anchor to its declared end.
	if fe.Offset < d.CorruptOffset || fe.Offset > d.CorruptEnd {
		t.Fatalf("fault offset %d outside block span [%d,%d]",
			fe.Offset, d.CorruptOffset, d.CorruptEnd)
	}
}

func TestTimestampRegressionVisible(t *testing.T) {
	raw, d := Build(defaultSpec(), CorruptTimestampRegression)
	intervals, err := ScanIntervals(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(intervals) != d.BlockCount {
		t.Fatalf("interval count %d want %d", len(intervals), d.BlockCount)
	}
	// On at least one track, a later block must precede an earlier block.
	regressed := false
	lastPerTrack := map[uint32]int64{}
	for _, iv := range intervals {
		if prev, ok := lastPerTrack[iv.Track]; ok && iv.AbsTimecode < prev {
			regressed = true
		}
		lastPerTrack[iv.Track] = iv.AbsTimecode
	}
	if !regressed {
		t.Fatal("no cross-cluster timestamp regression detected on any track")
	}
}
