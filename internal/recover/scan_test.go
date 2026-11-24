package recover

import (
	"testing"

	"archive/internal/essence"
	"archive/internal/fault"
	"archive/internal/fixture"
	"archive/internal/model"
	"archive/internal/probe"
)

func build(t *testing.T, name string) (fixture.Spec, *fixture.Layout, []byte) {
	t.Helper()
	spec, ok := fixture.FindSpec(fixture.DefaultMixed(), name)
	if !ok {
		t.Fatalf("spec %s", name)
	}
	var layout *fixture.Layout
	var data []byte
	var err error
	if spec.Format == model.ContainerMP4 {
		layout, data, err = fixture.BuildMP4(spec)
	} else {
		layout, data, err = fixture.BuildMXF(spec)
	}
	if err != nil {
		t.Fatal(err)
	}
	return spec, layout, data
}

func probeTrack(t *testing.T, data []byte, format model.ContainerKind, trackID uint32) (*probe.File, *model.Track) {
	t.Helper()
	f, err := probe.Probe(probe.Source{Name: "x", Data: data}, format)
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range f.Tracks {
		if tr.TrackID == trackID {
			return f, tr
		}
	}
	t.Fatalf("track %d not probed", trackID)
	return nil, nil
}

func TestScanRebuildsExactTimelineWithoutIndex(t *testing.T) {
	for _, name := range []string{"reel_a", "clip_b"} {
		_, layout, data := build(t, name)
		for _, tl := range layout.Tracks {
			_, tr := probeTrack(t, data, layout.Spec.Format, tl.Spec.TrackID)
			scan, faults := ScanTrack(data, tr)
			if len(faults) != 0 {
				t.Fatalf("%s/%s: faults %v", name, tl.Spec.Logical, probe.DescribeFaults(faults))
			}
			if len(scan.Frames) != tl.Spec.Frames {
				t.Fatalf("%s/%s: frames = %d, want %d", name, tl.Spec.Logical, len(scan.Frames), tl.Spec.Frames)
			}
			for i, fr := range scan.Frames {
				want := tl.Frames[i]
				if fr.StartTick != want.StartTick || fr.Duration != want.Duration {
					t.Fatalf("%s/%s frame %d ticks = %d/%d, want %d/%d", name, tl.Spec.Logical, i, fr.StartTick, fr.Duration, want.StartTick, want.Duration)
				}
				truthOff := want.Offset
				if layout.Spec.Format == model.ContainerMXF {
					truthOff += 20
				}
				if fr.Offset != truthOff {
					t.Fatalf("%s/%s frame %d offset = %d, want %d", name, tl.Spec.Logical, i, fr.Offset, truthOff)
				}
				if fr.State != StateComplete {
					t.Fatalf("%s/%s frame %d state = %s", name, tl.Spec.Logical, i, fr.State)
				}
				if err := checkPayload(fr); err != nil {
					t.Fatalf("%s/%s frame %d: %v", name, tl.Spec.Logical, i, err)
				}
			}
			var report model.TrackTimeline
			Rebuild(scan, &report)
			if !report.Complete {
				t.Fatalf("%s/%s: rebuilt timeline not complete: %+v", name, tl.Spec.Logical, report)
			}
			if report.Declared.StartNS != tr.Timebase.TickToNanos(tl.Spec.StartTick) {
				t.Fatalf("%s/%s: declared start %d", name, tl.Spec.Logical, report.Declared.StartNS)
			}
			// Localization error of the first frame must be exactly zero.
			if got := LocalizationError(tr.Timebase, report.Declared.StartNS, tr.Timebase.TickToNanos(tl.Spec.StartTick)); got != 0 {
				t.Fatalf("%s/%s: localization error %d", name, tl.Spec.Logical, got)
			}
		}
	}
}

func checkPayload(fr *Frame) error {
	if len(fr.Payload) == 0 {
		return nil
	}
	h, err := essence.ParseHeader(fr.Payload)
	if err != nil {
		return err
	}
	ok, complete := essence.VerifyPayload(fr.Payload, h)
	if !complete || !ok {
		return errBadPayload
	}
	return nil
}

var errBadPayload = &fault.Error{Code: fault.CorruptSegment, Detail: "payload"}

func TestScanFindsDamagedFrameAndReportsExactRange(t *testing.T) {
	_, layout, clean := build(t, "reel_a")
	plan, _, err := fixture.Plan(layout, []fixture.DamageKind{fixture.DamageSegment})
	if err != nil {
		t.Fatal(err)
	}
	damaged, injections, err := fixture.Apply(layout, clean, plan)
	if err != nil {
		t.Fatal(err)
	}
	inj := injections[0]
	_, tr := probeTrack(t, damaged, model.ContainerMXF, inj.TrackID)
	scan, faults := ScanTrack(damaged, tr)
	if len(scan.Frames) == 0 {
		t.Fatal("scan found no frames")
	}
	var damagedFrames []*Frame
	for _, fr := range scan.Frames {
		if fr.State != StateComplete {
			damagedFrames = append(damagedFrames, fr)
		}
	}
	if len(damagedFrames) == 0 {
		t.Fatalf("no damaged frame detected (complete=%d)", scan.Complete)
	}
	// The damaged frame's tick range must cover the injected range exactly
	// (the injection is inside one frame) and the localization error must be
	// zero ticks.
	covered := false
	for _, fr := range damagedFrames {
		startNS := tr.Timebase.TickToNanos(fr.StartTick)
		endNS := tr.Timebase.TickToNanos(fr.EndTick())
		if startNS <= inj.StartNS && endNS >= inj.EndNS {
			covered = true
			// Compare against the true frame start (known from the layout).
			if got := LocalizationError(tr.Timebase, startNS, inj.StartNS); got != 0 {
				t.Fatalf("localization error = %d ticks", got)
			}
		}
	}
	if !covered {
		t.Fatalf("damaged range not covered: frames=%v inj=[%d,%d]", frameRanges(damagedFrames, tr.Timebase), inj.StartNS, inj.EndNS)
	}
	if faults.ByCode(fault.CorruptSegment) == 0 {
		t.Fatal("no corrupt_segment fault")
	}
	var report model.TrackTimeline
	Rebuild(scan, &report)
	if report.Complete {
		t.Fatal("rebuilt timeline must not claim completeness")
	}
	if len(report.UnrecoveredNS) == 0 {
		t.Fatal("damaged interval not recorded as unrecovered")
	}
	// Recovery must not lose the good frames: exactly one track frame was hit.
	if scan.Complete != len(scan.Frames)-1 {
		t.Fatalf("complete=%d of %d", scan.Complete, len(scan.Frames))
	}
}

func frameRanges(frames []*Frame, tb model.Timebase) [][2]int64 {
	var out [][2]int64
	for _, f := range frames {
		out = append(out, [2]int64{tb.TickToNanos(f.StartTick), tb.TickToNanos(f.EndTick())})
	}
	return out
}

func TestScanTruncatedLastFrameIsPartialNotSilent(t *testing.T) {
	_, layout, data := build(t, "reel_a")
	last := uint64(0)
	var lastTrackID uint32
	for _, tl := range layout.Tracks {
		for _, fr := range tl.Frames {
			if fr.Offset+fr.Size > last {
				last = fr.Offset + fr.Size
				lastTrackID = tl.Spec.TrackID
			}
		}
	}
	cut := data[:last-7]
	_, tr := probeTrack(t, cut, model.ContainerMXF, lastTrackID)
	scan, faults := ScanTrack(cut, tr)
	if scan.Partial == 0 {
		t.Fatalf("no partial frame: %+v", scanSummary(scan))
	}
	if faults.ByCode(fault.TruncatedBox) == 0 {
		t.Fatal("no truncated_box fault")
	}
	var report model.TrackTimeline
	Rebuild(scan, &report)
	if report.Complete {
		t.Fatal("truncated scan claimed complete")
	}
	for _, e := range faults {
		if e.Code == fault.TruncatedBox {
			if !e.HasRange {
				t.Fatalf("truncated fault lacks a range: %v", e.Line())
			}
			// The reported offset must be at or before the cut and inside
			// the file.
			if e.Offset >= uint64(len(cut)) {
				t.Fatalf("fault offset %d outside file %d", e.Offset, len(cut))
			}
		}
	}
}

func scanSummary(s *TrackScan) string { return Summary(s) }

func TestScanMultipleTracksAreSeparated(t *testing.T) {
	_, layout, data := build(t, "reel_a")
	ids := map[uint32]bool{}
	for _, tl := range layout.Tracks {
		_, tr := probeTrack(t, data, model.ContainerMXF, tl.Spec.TrackID)
		scan, _ := ScanTrack(data, tr)
		if len(scan.Frames) != tl.Spec.Frames {
			t.Fatalf("track %d found %d of %d frames", tl.Spec.TrackID, len(scan.Frames), tl.Spec.Frames)
		}
		for _, fr := range scan.Frames {
			if fr.TrackID != tl.Spec.TrackID {
				t.Fatalf("cross-track frame leaked into %d", tl.Spec.TrackID)
			}
		}
		ids[tl.Spec.TrackID] = true
	}
	if len(ids) != len(layout.Tracks) {
		t.Fatalf("ids = %v", ids)
	}
}

func TestPlanChoosesScanWhenIndexIncomplete(t *testing.T) {
	_, layout, data := build(t, "clip_b")
	lean := layout.Spec
	lean.Tracks = append([]fixture.TrackSpec(nil), layout.Spec.Tracks...)
	for i := range lean.Tracks {
		lean.Tracks[i].DropsIndex = true
	}
	_, leanData, err := fixture.BuildMP4(lean)
	if err != nil {
		t.Fatal(err)
	}
	f, err := probe.Probe(probe.Source{Name: "x", Data: leanData}, model.ContainerMP4)
	if err != nil {
		t.Fatal(err)
	}
	need, trusted := Plan(f)
	if len(need) != len(lean.Tracks) || len(trusted) != 0 {
		t.Fatalf("need=%d trusted=%d", len(need), len(trusted))
	}
	// With complete indexes everything is trusted.
	f2, err := probe.Probe(probe.Source{Name: "y", Data: data}, model.ContainerMP4)
	if err != nil {
		t.Fatal(err)
	}
	need2, trusted2 := Plan(f2)
	if len(need2) != 0 || len(trusted2) != len(layout.Tracks) {
		t.Fatalf("complete file: need=%d trusted=%d", len(need2), len(trusted2))
	}
}

func TestPlanRescansWhenIndexPointsPastEOF(t *testing.T) {
	_, layout, data := build(t, "clip_b")
	// Cut the media so the complete index now points past EOF.
	cut := data[:len(data)-100]
	f, err := probe.Probe(probe.Source{Name: "x", Data: cut}, model.ContainerMP4)
	if err != nil {
		t.Fatal(err)
	}
	need, trusted := Plan(f)
	if len(need) == 0 {
		t.Fatalf("index pointing past EOF was trusted (trusted=%d, layout=%d)", len(trusted), len(layout.Tracks))
	}
}

func TestGapsAreReported(t *testing.T) {
	// Build a synthetic track where the middle frame is deleted entirely.
	spec := fixture.TrackSpec{
		Logical: "video", Container: model.ContainerMP4, TrackID: 1, Kind: model.KindVideo,
		Timescale: 1000, FrameTicks: 40, Frames: 5, PayloadLen: 32, SyncEvery: 1,
	}
	var joined []byte
	for i := 0; i < spec.Frames; i++ {
		if i == 2 {
			continue
		}
		joined = append(joined, fixture.BuildFrame(spec, i)...)
	}
	tr := &model.Track{Key: "mp4/t1", TrackID: 1, Kind: model.KindVideo, Timebase: model.Timebase{Timescale: 1000}}
	scan, _ := ScanTrack(joined, tr)
	if len(scan.Frames) != 4 {
		t.Fatalf("frames = %d", len(scan.Frames))
	}
	if len(scan.Gaps) != 1 {
		t.Fatalf("gaps = %v", scan.Gaps)
	}
	if scan.Gaps[0].StartNS != 80000000 || scan.Gaps[0].EndNS != 120000000 {
		t.Fatalf("gap = %+v", scan.Gaps[0])
	}
	var report model.TrackTimeline
	Rebuild(scan, &report)
	if report.Complete {
		t.Fatal("gap must make the timeline incomplete")
	}
	found := false
	for _, u := range report.UnrecoveredNS {
		if u.StartNS == 80000000 && u.EndNS == 120000000 {
			found = true
		}
	}
	if !found {
		t.Fatalf("gap not reported as unrecovered: %+v", report.UnrecoveredNS)
	}
}
