package pipeline

import (
	"testing"

	"mediamux/media"
)

func ref(id string, ms int64, off int64) SegmentRef {
	return SegmentRef{ID: id, Duration: media.NewTime(ms*90, media.Timescale90K), DeclaredOffset: off}
}

func TestCompareManifestToMediaDetectsDrift(t *testing.T) {
	refs := []SegmentRef{ref("a", 5000, 99), ref("b", 5000, 140)}
	actual := []int64{2002, 2002}
	rep := CompareManifestToMedia(refs, actual, 2)
	if rep.Aligned() {
		t.Fatal("drift not detected")
	}
	if len(rep.Mismatches) != 2 || rep.Observed != 2 {
		t.Fatalf("report=%+v", rep)
	}
	m := rep.Mismatches[0]
	if m.Segment != 0 || m.SegmentID != "a" || m.DeclaredMS != 5000 || m.ActualMS != 2002 {
		t.Fatalf("mismatch=%+v", m)
	}
	if m.DeltaMS != 2998 || m.ManifestOffset != 99 {
		t.Fatalf("mismatch offsets=%+v", m)
	}
	if rep.TotalDeltaMS != 5996 || rep.MaxDeltaMS != 2998 {
		t.Fatalf("totals=%+v", rep)
	}
}

func TestCompareManifestToMediaWithinTolerance(t *testing.T) {
	refs := []SegmentRef{ref("a", 6005, 10)}
	rep := CompareManifestToMedia(refs, []int64{6006}, 2)
	if !rep.Aligned() {
		t.Fatalf("1ms delta should be within tolerance: %+v", rep.Mismatches)
	}
	if rep.MaxDeltaMS != 1 {
		t.Fatalf("max=%d", rep.MaxDeltaMS)
	}
}

func TestCompareManifestToMediaSkipsUnknownDurations(t *testing.T) {
	refs := []SegmentRef{ref("a", 6000, 0), ref("b", 6000, 40)}
	rep := CompareManifestToMedia(refs, []int64{0, 6000}, 2)
	if len(rep.Mismatches) != 0 || rep.Observed != 1 {
		t.Fatalf("report=%+v", rep)
	}
}

func TestMediaDurationsMSParallelToManifest(t *testing.T) {
	outs := []SegmentOutcome{
		{Index: 2, Quality: analyzeSample(3000)},
		{Index: 0, Quality: analyzeSample(1000)},
	}
	got := MediaDurationsMS(outs, 4)
	want := []int64{1000, 0, 3000, 0}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got=%v want=%v", got, want)
		}
	}
}

func TestMaskSegmentsCoversCommittedRange(t *testing.T) {
	doc := manifestDoc(3)
	got := MaskSegments(doc, 0, 2)
	if len(got.Segments) != 2 {
		t.Fatalf("masked=%d", len(got.Segments))
	}
	if len(doc.Segments) != 3 {
		t.Fatal("MaskSegments mutated the source document")
	}
}
