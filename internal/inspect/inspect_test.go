package inspect

import (
	"os"
	"path/filepath"
	"testing"

	"archivecheck/internal/fixture"
	"archivecheck/internal/manifest"
	"archivecheck/internal/model"
	"archivecheck/internal/transport"
)

// genArchive builds a throwaway fixture for inspection tests.
func genArchive(t *testing.T, fault fixture.Fault) (string, *fixture.Manifest) {
	t.Helper()
	root := t.TempDir()
	res, err := fixture.Generate(fixture.Config{Seed: 42, Root: root, Fault: fault})
	if err != nil {
		t.Fatalf("generate fixture: %v", err)
	}
	return root, res.Manifest
}

func loadFragment(t *testing.T, root, initRel, segRel string) (*Init, *Fragment) {
	t.Helper()
	initData, err := os.ReadFile(filepath.Join(root, initRel))
	if err != nil {
		t.Fatal(err)
	}
	init, err := ParseInit(initRel, initData)
	if err != nil {
		t.Fatalf("ParseInit: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, segRel))
	if err != nil {
		t.Fatal(err)
	}
	frag, err := ParseFragment(segRel, data, nil, Options{})
	if err != nil {
		// Structural faults are reported, not fatal.
		if frag == nil {
			t.Fatalf("ParseFragment: %v", err)
		}
	}
	frag.LoadSamples(init, Options{})
	return init, frag
}

func TestParseFragmentStructureAndSamples(t *testing.T) {
	root, m := genArchive(t, fixture.FaultNone)
	info, _ := m.SegmentInfoFor("v-high", "1")
	_, frag := loadFragment(t, root, "v-high/init.mp4", info.URI)
	if len(frag.Boxes) != 3 || frag.Boxes[0].Type != "styp" || frag.Boxes[1].Type != "moof" || frag.Boxes[2].Type != "mdat" {
		t.Fatalf("boxes %+v", frag.Boxes)
	}
	if len(frag.Tracks) != 1 || len(frag.Tracks[0].Samples) != fixture.DefaultFramesPerSeg {
		t.Fatalf("tracks %+v", frag.Tracks)
	}
	tr := frag.Tracks[0]
	if len(tr.SyncSamples) != 4 {
		t.Fatalf("sync samples %v", tr.SyncSamples)
	}
	for i, s := range tr.Samples {
		want := uint64(i) * uint64(fixture.DefaultFrameTicks)
		if s.DecodeTime != want {
			t.Fatalf("sample %d decode=%d want %d", i, s.DecodeTime, want)
		}
		if s.Sync != (i%fixture.DefaultKeyframeGap == 0) {
			t.Fatalf("sample %d sync=%v", i, s.Sync)
		}
	}
	if len(frag.Faults) != 0 {
		t.Fatalf("clean fragment reported %v", frag.Faults)
	}
}

func TestTruncatedBoxLocalisesByteOffset(t *testing.T) {
	root, m := genArchive(t, fixture.FaultTruncatedBox)
	info, _ := m.SegmentInfoFor("v-low", "7")
	_, frag := loadFragment(t, root, "v-low/init.mp4", info.URI)
	if frag.Truncated == nil {
		t.Fatal("expected a truncation fault")
	}
	f := frag.Truncated
	if f.Code != model.CodeMP4BoxTruncated {
		t.Fatalf("code=%s", f.Code)
	}
	if !f.Location.ByteSet || f.Location.ByteEnd <= f.Location.ByteStart {
		t.Fatalf("bytes %+v", f.Location)
	}
	if f.Source.File != "v-low/seg-06.m4s" {
		t.Fatalf("file=%s", f.Source.File)
	}
	// The mdat header is at 1708; the truncation is inside its payload.
	if f.Location.ByteStart != 1708 {
		t.Fatalf("byte start=%d want 1708", f.Location.ByteStart)
	}
	// The exactly-truncated byte must be the segment size.
	if info.Size != f.Location.ByteEnd {
		t.Fatalf("declared end=%d file size=%d", f.Location.ByteEnd, info.Size)
	}
}

func TestSampleTableTruncationPointsAtByte(t *testing.T) {
	// A segment whose mdat declares more bytes than exist also makes the last
	// samples unreachable; the first missing sample is localised precisely.
	root, m := genArchive(t, fixture.FaultTruncatedBox)
	info, _ := m.SegmentInfoFor("v-low", "7")
	initData, err := os.ReadFile(filepath.Join(root, "v-low/init.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	init, err := ParseInit("init.mp4", initData)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, info.URI))
	if err != nil {
		t.Fatal(err)
	}
	frag, _ := ParseFragment(info.URI, data, nil, Options{})
	frag.LoadSamples(init, Options{})
	if len(frag.Tracks) != 1 {
		t.Fatalf("tracks %d", len(frag.Tracks))
	}
	// Every decoded sample must lie inside the bytes that actually exist.
	for _, s := range frag.Tracks[0].Samples {
		if s.DataOffset+int64(s.Size) > int64(len(data)) {
			t.Fatalf("sample at %d+%d exceeds file size %d", s.DataOffset, s.Size, len(data))
		}
	}
}

func TestNonMonotonicTimestampsAreFaults(t *testing.T) {
	root, m := genArchive(t, fixture.FaultNone)
	info, _ := m.SegmentInfoFor("v-high", "1")
	_, frag := loadFragment(t, root, "v-high/init.mp4", info.URI)
	// Force a non-monotonic decode timeline and re-check.
	tr := &frag.Tracks[0]
	tr.Samples[5].DecodeTime = tr.Samples[4].DecodeTime - 1
	frag.Faults = nil
	detectNonMonotonic(frag)
	if len(frag.Faults) != 1 {
		t.Fatalf("faults %+v", frag.Faults)
	}
	f := frag.Faults[0]
	if f.Code != model.CodeMP4NonMonotonic || !f.Location.ByteSet {
		t.Fatalf("fault %+v", f)
	}
}

func TestKeyframeIntervals(t *testing.T) {
	root, m := genArchive(t, fixture.FaultNone)
	info, _ := m.SegmentInfoFor("v-high", "1")
	_, frag := loadFragment(t, root, "v-high/init.mp4", info.URI)
	rep := KeyframeIntervals(frag, fixture.DefaultTimebase)
	if rep.P50Ms != 1000 {
		t.Fatalf("p50=%d rem=%s", rep.P50Ms, rep.P50Remainder)
	}
	if len(rep.Intervals) != 3 {
		t.Fatalf("intervals=%d", len(rep.Intervals))
	}
	for _, iv := range rep.Intervals {
		if iv.Ticks != uint64(fixture.DefaultKeyframeGap*fixture.DefaultFrameTicks) {
			t.Fatalf("interval %+v", iv)
		}
	}
}

func TestParseInitRejectsZeroTimescale(t *testing.T) {
	if _, err := ParseInit("x.mp4", []byte("not an mp4")); err == nil {
		t.Fatal("garbage init must fail")
	}
}

func TestMapGapToMediaTime(t *testing.T) {
	// At 90 kHz, one RTP unit is one plan tick. The anchor pins RTP 3200 to
	// the start of segment 4, so the damaged interval begins 3200 ticks in.
	gap := transport.Gap{Before: 10, After: 20, Count: 9, FirstTimestamp: 3200 + 3200, LastTimestamp: 3200 + 3200 + 36}
	dr, fault := MapGap(manifest.Segment{ID: "4", Start: 3 * fixture.DefaultSegmentTicks, Duration: 1000000}, "v-high", "v-high/seg-03.m4s", gap, GapAnchor{RTPTimestamp: 3200, Media: 3 * fixture.DefaultSegmentTicks, RTPTimebase: fixture.DefaultRTPClock}, fixture.DefaultTimebase)
	if fault == nil || fault.Code != model.CodeDamageGap {
		t.Fatalf("fault %+v", fault)
	}
	if dr.Confidence != ConfidenceExact {
		t.Fatalf("confidence=%s", dr.Confidence)
	}
	if dr.Start != 3*fixture.DefaultSegmentTicks+3200 || dr.End != 3*fixture.DefaultSegmentTicks+3236 {
		t.Fatalf("range %+v", dr)
	}
	if !fault.Location.MediaSet || fault.Source.Segment != "4" {
		t.Fatalf("fault location %+v", fault)
	}
}

func TestMapGapFallsBackToSegmentBounds(t *testing.T) {
	seg := manifest.Segment{ID: "2", Start: 9000, Duration: 3000}
	anchor := GapAnchor{RTPTimestamp: 0, Media: 0, RTPTimebase: 0}
	dr, _ := MapGap(seg, "v", "v/2.m4s", transport.Gap{Count: 1, FirstTimestamp: 5, LastTimestamp: 9}, anchor, 90000)
	if dr.Confidence != ConfidenceUnknown {
		t.Fatalf("confidence=%s", dr.Confidence)
	}
	if dr.Start != 9000 || dr.End != 12000 {
		t.Fatalf("range %+v", dr)
	}
}

func TestMergeDamageAcrossBoundary(t *testing.T) {
	frame := int64(3600)
	ranges := []DamageRange{
		{Start: 3600, End: 7200, Segments: []string{"4"}, Representations: []string{"v-low"}, Causes: []model.Code{model.CodeTransportGap}, Confidence: ConfidenceExact},
		{Start: 7200, End: 9000, Segments: []string{"5"}, Representations: []string{"v-low"}, Causes: []model.Code{model.CodeTransportGap}, Confidence: ConfidenceExact},
		{Start: 100000, End: 104000, Segments: []string{"9"}, Representations: []string{"v-low"}, Confidence: ConfidenceExact},
	}
	merged := MergeDamage(ranges, MergeOptions{FrameDuration: frame, Timebase: 90000})
	if len(merged) != 2 {
		t.Fatalf("merged=%+v", merged)
	}
	if merged[0].Start != 3600 || merged[0].End != 9000 {
		t.Fatalf("first %+v", merged[0])
	}
	if len(merged[0].Segments) != 2 {
		t.Fatalf("segments %v", merged[0].Segments)
	}
}

func TestMergeDamageSeparatedByMoreThanFrame(t *testing.T) {
	ranges := []DamageRange{
		{Start: 0, End: 1000},
		{Start: 1000 + 3601, End: 9000},
	}
	merged := MergeDamage(ranges, MergeOptions{FrameDuration: 3600})
	if len(merged) != 2 {
		t.Fatalf("ranges separated by more than one frame must stay apart: %+v", merged)
	}
	// Exactly one frame apart merges.
	ranges[1].Start = 1000 + 3600
	if got := MergeDamage(ranges, MergeOptions{FrameDuration: 3600}); len(got) != 1 {
		t.Fatalf("one-frame separation should merge: %+v", got)
	}
}

func TestFragmentSizeLimitIsEnforced(t *testing.T) {
	root, _ := genArchive(t, fixture.FaultNone)
	path := filepath.Join(root, "v-high", "seg-00.m4s")
	fd, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer fd.Close()
	_, err = SizeReader{R: fd, Limit: 1024}.ReadAll()
	if err == nil {
		t.Fatal("segment above the limit must be refused")
	}
}
