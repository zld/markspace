package mp4

import (
	"path/filepath"
	"testing"

	"mediad/internal/clock"
	"mediad/internal/fixture"
)

func scanAndRepair(t *testing.T, corrupt string) (*Repaired, *fixture.Session) {
	t.Helper()
	dir := t.TempDir()
	sess, err := fixture.Generate(dir, 42, 8_000_000, corrupt)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ScanFile(filepath.Join(dir, "media", "cam-a.mp4"))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	specs := map[uint32]TrackSpec{}
	for _, e := range sess.Manifest.Entries {
		if e.Camera != "cam-a" {
			continue
		}
		for _, ts := range e.Tracks {
			// Repair is per-file; cam-a's origin is its recording start.
			specs[ts.TrackID] = TrackSpec{Timescale: ts.Timescale, OriginNs: e.Recording.StartNs}
		}
	}
	rep, err := Repair(s, RepairOptions{TrackSpecs: specs, RecordingOriginNs: 0})
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	return rep, sess
}

func TestRepairCleanKeepsOffsetsAndTimeline(t *testing.T) {
	rep, _ := scanAndRepair(t, fixture.CorruptNone)
	tr := rep.Tracks[0]
	if tr.RepairCount != 0 {
		t.Fatalf("clean media repaired %d samples", tr.RepairCount)
	}
	if tr.FirstNs != 0 {
		t.Fatalf("cam-a should begin at presentation zero, got %d", tr.FirstNs)
	}
	// Last presented sample is just under 12 s.
	if tr.LastNs < 11_900_000_000 || tr.LastNs > 12_100_000_000 {
		t.Fatalf("last presentation %d outside expected window", tr.LastNs)
	}
	// DTS must be strictly increasing after repair.
	for i := 1; i < len(tr.Samples); i++ {
		if tr.Samples[i].DTS <= tr.Samples[i-1].DTS {
			t.Fatalf("DTS not monotonic at %d", i)
		}
	}
	// Every source offset is retained unchanged.
	raw, err := ScanFile(rep.Path)
	if err != nil {
		t.Fatal(err)
	}
	for i := range tr.Samples {
		if tr.Samples[i].Offset != raw.Tracks[0].Samples[i].Offset {
			t.Fatalf("offset changed at %d", i)
		}
		if tr.Samples[i].RawOffset != raw.Tracks[0].Samples[i].TableOffset {
			t.Fatalf("table offset lost at %d", i)
		}
	}
}

func TestRepairPreservesTrackOrigin(t *testing.T) {
	dir := t.TempDir()
	sess, err := fixture.Generate(dir, 42, 8_000_000, fixture.CorruptNone)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ScanFile(filepath.Join(dir, "media", "cam-b.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	specs := map[uint32]TrackSpec{1: {Timescale: 90000, OriginNs: 250_000_000}}
	rep, err := Repair(s, RepairOptions{TrackSpecs: specs})
	if err != nil {
		t.Fatal(err)
	}
	tr := rep.Tracks[0]
	// elst media_time is 4500 ticks = 50 ms of stored pre-roll before the
	// recording origin. The first sample at or after that trim point must land
	// exactly on 250 ms; earlier pre-roll samples keep negative presentation
	// times rather than being silently dropped.
	trim := tr.EditList.FirstMediaTime()
	if trim != 3600 {
		t.Fatalf("edit list media_time=%d", trim)
	}
	// The trim point itself maps exactly to the origin; the first sample at or
	// after it sits within one sample period (33.3 ms) of the origin.
	if ns, ok := tr.EditList.PresentationNs(trim); !ok || ns != 0 {
		t.Fatalf("trim point maps to %d (ok=%v)", ns, ok)
	}
	// Sample NS is file-relative, so the first presented sample sits within
	// one frame of zero after the trim.
	var firstPresented int64 = -1
	for _, smp := range tr.Samples {
		if smp.PTS >= trim {
			firstPresented = smp.NS
			break
		}
	}
	if firstPresented < 0 || firstPresented > 34_000_000 {
		t.Fatalf("first presented sample ns=%d outside one frame of zero", firstPresented)
	}
	if tr.FirstNs != 250_000_000 {
		t.Fatalf("presented origin=%d want 250ms", tr.FirstNs)
	}
	if tr.Samples[0].PTS >= trim {
		t.Fatalf("fixture expected pre-roll before the trim point, PTS=%d trim=%d", tr.Samples[0].PTS, trim)
	}
	_ = sess
}

// corruptDTS rewrites the fixture's raw samples to simulate a timestamp jump
// without touching files: repair must correct the stored table.
func TestRepairFixesDTSRegression(t *testing.T) {
	dir := t.TempDir()
	_, err := fixture.Generate(dir, 42, 8_000_000, fixture.CorruptNone)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ScanFile(filepath.Join(dir, "media", "cam-a.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	raw := s.Tracks[0]
	// Drop sample 100's DTS by a full second (a capture-card glitch) and shift
	// the rest of the run down; repair must detect and bridge it.
	base := raw.Samples[100].DTS
	for i := 100; i < 130; i++ {
		raw.Samples[i].DTS = base - int64(i-99)*3000
	}
	s.Tracks[0] = raw
	specs := map[uint32]TrackSpec{1: {Timescale: 90000}}
	rep, err := Repair(s, RepairOptions{TrackSpecs: specs})
	if err != nil {
		t.Fatal(err)
	}
	tr := rep.Tracks[0]
	if tr.RepairCount == 0 {
		t.Fatal("expected repairs for DTS regression")
	}
	foundCause := false
	for _, iv := range tr.Intervals {
		if iv.Cause == clock.CodeDTSRegression && iv.RawOffset > 0 {
			foundCause = true
		}
	}
	if !foundCause {
		t.Fatalf("no dts_regression interval with offset: %+v", tr.Intervals)
	}
	for i := 1; i < len(tr.Samples); i++ {
		if tr.Samples[i].DTS <= tr.Samples[i-1].DTS {
			t.Fatalf("regression survived at %d", i)
		}
	}
}

func TestRepairUnrepairableGapReportsOffset(t *testing.T) {
	dir := t.TempDir()
	_, err := fixture.Generate(dir, 42, 8_000_000, fixture.CorruptNone)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ScanFile(filepath.Join(dir, "media", "cam-a.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	raw := s.Tracks[0]
	// Insert a 60 s hole, beyond the 20 s policy.
	for i := 200; i < len(raw.Samples); i++ {
		raw.Samples[i].DTS += 60 * 90000
	}
	s.Tracks[0] = raw
	_, err = Repair(s, RepairOptions{TrackSpecs: map[uint32]TrackSpec{1: {Timescale: 90000}}, Policy: DefaultPolicy()})
	if err == nil {
		t.Fatal("expected unrepairable range")
	}
	pe, ok := clock.As(err)
	if !ok || pe.Code != clock.CodeUnrepairableRange {
		t.Fatalf("unexpected error: %v", err)
	}
	if pe.RawOffset <= 0 || pe.Sample == 0 {
		t.Fatalf("missing offset/sample: %+v", pe)
	}
}

func TestRTPWrapExtension(t *testing.T) {
	dir := t.TempDir()
	sess, err := fixture.Generate(dir, 42, 8_000_000, fixture.CorruptRTPWrap)
	if err != nil {
		t.Fatal(err)
	}
	recs, err := fixture.ReadRTP(sess.RTPFiles["cam-a"])
	if err != nil {
		t.Fatal(err)
	}
	var rr []RTPRecord
	for _, r := range recs {
		rr = append(rr, RTPRecord{Timestamp: r.Timestamp, CaptureNs: r.CaptureNs, SSRC: r.SSRC})
	}
	a := extendRTP(rr)
	if a == nil || a.WrapCount != 1 {
		t.Fatalf("wrap count=%v", a)
	}
	if a.ExtendedLast <= uint64(a.FirstTimestamp) {
		t.Fatalf("extension did not recover monotonic time: first=%d last=%d", a.FirstTimestamp, a.ExtendedLast)
	}
	// Total elapsed is exactly (n-1) nominal steps.
	want := uint64(len(rr)-1) * 3000
	if a.ExtendedLast-uint64(rr[0].Timestamp) != want {
		t.Fatalf("elapsed=%d want %d", a.ExtendedLast-uint64(rr[0].Timestamp), want)
	}
}
