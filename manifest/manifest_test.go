package manifest

import (
	"testing"

	"mediad/render"
	"mediad/timeline"
)

func testTimeline() *timeline.Timeline {
	return &timeline.Timeline{
		Tracks: []timeline.Track{{ID: 1}, {ID: 2}},
		Events: []timeline.Event{{TrackID: 1, TimeNS: 0}, {TrackID: 2, TimeNS: 40_000_000}},
		Stats:  timeline.Stats{TrackCount: 2, EventCount: 2},
	}
}

func metrics(i int64, tns int64, bad bool) render.FrameMetrics {
	return render.FrameMetrics{Index: i, TimeNS: tns, SHA256: string(rune('a'+i%26)) + "hash",
		Bytes: 12000, BitrateBPS: 2_400_000, Quality: render.QualityStats{Mean: 100, Variance: 25}, Bad: bad,
		FirstBadTime: func() int64 {
			if bad {
				return tns
			}
			return 0
		}()}
}

func buildComplete(t *testing.T, fps int, n int, badAt int64) Manifest {
	t.Helper()
	b := NewBuilder(fps, 2_400_000, testTimeline())
	for i := int64(0); i < int64(n); i++ {
		tns := i * 1_000_000_000 / int64(fps)
		if err := b.AddFrame(metrics(i, tns, i == badAt)); err != nil {
			t.Fatal(err)
		}
	}
	if badAt >= 0 {
		b.AddError(ErrorRecord{TrackID: 1, TimeNS: badAt * 1_000_000_000 / int64(fps), Kind: "video", Reason: "corrupt sample"})
	}
	duration := int64(n) * 1_000_000_000 / int64(fps)
	rep := render.AggregateBitrate(int64(12000*n), duration, 2_400_000)
	return b.Finalize(duration, rep)
}

func TestManifestCompleteAndHashStable(t *testing.T) {
	m1 := buildComplete(t, 25, 300, -1)
	m2 := buildComplete(t, 25, 300, -1)
	if m1.State != StateComplete || m1.SHA256 == "" {
		t.Fatalf("complete manifest expected, got %+v", m1)
	}
	if m1.SHA256 != m2.SHA256 {
		t.Fatal("identical reruns must produce identical manifest hash")
	}
	if m1.MaxErrorNS != 0 {
		t.Fatalf("aligned frames must report zero timing error, got %d", m1.MaxErrorNS)
	}
	if err := Verify(Inputs{Timeline: testTimeline(), Manifest: m1, FrameCount: 300}); err != nil {
		t.Fatalf("verification failed: %v", err)
	}
}

func TestMisalignedFrameTimeRejected(t *testing.T) {
	m := buildComplete(t, 25, 10, -1)
	// Tamper one frame time by 2ns: exceeds the 1ns tolerance.
	m.Frames[3].TimeNS += 2
	m.SHA256 = hashManifest(m)
	err := Verify(Inputs{Timeline: testTimeline(), Manifest: m, FrameCount: 10})
	if err == nil {
		t.Fatal("misaligned frame must fail verification")
	}
	ve, ok := err.(*VerificationError)
	if !ok || ve.Op != "timebase" {
		t.Fatalf("want timebase error, got %v", err)
	}
	// A 1ns shift is still within tolerance but the hash change is caught first.
	ok2 := buildComplete(t, 25, 10, -1)
	ok2.Frames[3].TimeNS += 1
	ok2.SHA256 = hashManifest(ok2)
	if err := Verify(Inputs{Timeline: testTimeline(), Manifest: ok2, FrameCount: 10}); err != nil {
		t.Fatalf("1ns shift within tolerance must pass timebase: %v", err)
	}
}

func TestDuplicateItemsRejected(t *testing.T) {
	b := NewBuilder(25, 2_400_000, testTimeline())
	_ = b.AddFrame(metrics(0, 0, false))
	if err := b.AddFrame(metrics(0, 40_000_000, false)); err == nil {
		t.Fatal("duplicate index must be rejected at build time")
	}
	b2 := NewBuilder(25, 2_400_000, testTimeline())
	_ = b2.AddFrame(metrics(0, 0, false))
	_ = b2.AddFrame(metrics(1, 40_000_000, false))
	m := b2.Finalize(80_000_000, render.BitrateReport{TargetBPS: 2_400_000})
	m.Frames = append(m.Frames, m.Frames[1]) // duplicate time after finalize
	m.SHA256 = hashManifest(m)
	if err := Verify(Inputs{Timeline: testTimeline(), Manifest: m, FrameCount: 3}); err == nil {
		t.Fatal("duplicate frame time must fail verification")
	}
}

func TestCorruptStateAndRecoveryNoDupes(t *testing.T) {
	// First run hits a corrupt keyframe at frame 40 and stops the track.
	corrupt := buildComplete(t, 25, 60, 40)
	if corrupt.State != StateCorrupt || len(corrupt.Errors) != 1 {
		t.Fatalf("corrupt manifest expected, state=%s errors=%d", corrupt.State, len(corrupt.Errors))
	}
	if corrupt.Frames[40].FirstBadTime == 0 {
		t.Fatal("first bad time must be recorded")
	}
	// Recovery run rebuilds only frames >= 60 and merges without duplicates.
	b := NewBuilder(25, 2_400_000, testTimeline())
	for i := int64(0); i < 60; i++ { // replay the already-completed prefix
		if err := b.AddFrame(metrics(i, i*40_000_000, i == 40)); err != nil {
			t.Fatal(err)
		}
	}
	for i := int64(60); i < 300; i++ {
		if err := b.AddFrame(metrics(i, i*40_000_000, false)); err != nil {
			t.Fatal(err)
		}
	}
	duration := int64(300) * 40_000_000
	rep := render.AggregateBitrate(int64(12000*300), duration, 2_400_000)
	recovered := b.Finalize(duration, rep)
	if len(recovered.Frames) != 300 {
		t.Fatalf("recovery must yield 300 unique frames, got %d", len(recovered.Frames))
	}
	idx0 := recovered.Frames[0]
	idx299 := recovered.Frames[299]
	if idx0.Index != 0 || idx299.Index != 299 {
		t.Fatal("recovered manifest must preserve full ordered frame range")
	}
}

func TestConservationAndIncompleteRejection(t *testing.T) {
	m := buildComplete(t, 25, 10, -1)
	good := Inputs{Timeline: testTimeline(), Manifest: m, FrameCount: 10}
	if err := Verify(good); err != nil {
		t.Fatal(err)
	}
	badCount := good
	badCount.FrameCount = 9
	if err := Verify(badCount); err == nil {
		t.Fatal("frame count divergence must fail")
	}
	badEvents := good
	badEvents.Manifest.EventCount = 99
	badEvents.Manifest.SHA256 = hashManifest(badEvents.Manifest)
	if err := Verify(badEvents); err == nil {
		t.Fatal("event count divergence must fail")
	}
	badOrder := good
	badOrder.Manifest.TrackIDs = []uint32{2, 1}
	badOrder.Manifest.SHA256 = hashManifest(badOrder.Manifest)
	if err := Verify(badOrder); err == nil {
		t.Fatal("track order divergence must fail")
	}
	incomplete := good
	incomplete.Manifest.State = StateIncomplete
	if err := Verify(incomplete); err == nil {
		t.Fatal("incomplete manifest must be rejected")
	}
}
