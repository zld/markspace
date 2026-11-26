package repair

import (
	"testing"

	"rtx/internal/cmaf"
	"rtx/internal/model"
)

func mediaRate(t *testing.T) (model.Rate, map[string]int64) {
	t.Helper()
	r, err := model.NewRateRatio(720000, 1)
	if err != nil {
		t.Fatal(err)
	}
	return r, map[string]int64{"a": 0}
}

func frag(t *testing.T, source, path string, seq *uint32, videoTFD, audioTFD uint64, videoDur, audioDur uint32) Input {
	t.Helper()
	video := []cmaf.MuxSample{{Data: []byte("frame"), Duration: videoDur, Key: true}}
	audio := []cmaf.MuxSample{{Data: make([]byte, 8), Duration: audioDur}}
	raw, err := cmaf.BuildFragment(cmaf.FragmentSpec{
		Sequence: 1, VideoTFD: videoTFD, AudioTFD: audioTFD, Video: video, Audio: audio,
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := cmaf.ParseBytes(path, raw)
	if err != nil {
		t.Fatal(err)
	}
	f, err := cmaf.ParseFragment(parsed)
	if err != nil {
		t.Fatal(err)
	}
	f.Source = source
	if seq == nil {
		f.MFHDSequence = nil
	} else {
		v := *seq
		f.MFHDSequence = &v
	}
	return Input{Source: source, Frag: f, Order: int(seq0(seq))}
}

func seq0(p *uint32) uint32 {
	if p == nil {
		return 0
	}
	return *p
}

func u32(v uint32) *uint32 { return &v }

func TestTimelineCleanIsUnchanged(t *testing.T) {
	r, off := mediaRate(t)
	tl, err := BuildTimeline(r, DefaultTrackSpecs(), off, []Input{
		frag(t, "a", "f1.m4s", u32(1), 0, 0, 3600, 1920),
		frag(t, "a", "f2.m4s", u32(2), 3600, 1920, 3600, 1920),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tl.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(tl.Repairs) != 0 {
		t.Fatalf("repairs = %+v", tl.Repairs)
	}
	if len(tl.Fragments) != 2 {
		t.Fatalf("fragments = %d", len(tl.Fragments))
	}
}

func TestTimelineRollbackIsRepaired(t *testing.T) {
	r, off := mediaRate(t)
	tl, err := BuildTimeline(r, DefaultTrackSpecs(), off, []Input{
		frag(t, "a", "f1.m4s", u32(1), 0, 0, 3600, 1920),
		frag(t, "a", "f2.m4s", u32(2), 1800, 1920, 3600, 1920),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tl.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(tl.Repairs) != 1 {
		t.Fatalf("repairs = %d, want 1", len(tl.Repairs))
	}
	rep := tl.Repairs[0]
	if rep.Original != 1800 || rep.PreviousEnd != 3600 || rep.TrackID != 1 {
		t.Fatalf("repair = %+v", rep)
	}
	if rep.Repaired != 3600 {
		t.Fatalf("repaired = %d, want 3600", rep.Repaired)
	}
	if rep.ByteOffset <= 0 || rep.Source != "a" || rep.Fragment != "f2.m4s" || rep.Reason != "tfdt_rollback" {
		t.Fatalf("repair attribution = %+v", rep)
	}
	var second *TrackSpan
	for _, ft := range tl.Fragments {
		for i := range ft.Tracks {
			if ft.Fragment.Path == "f2.m4s" && ft.Tracks[i].TrackID == 1 {
				second = &ft.Tracks[i]
			}
		}
	}
	if second == nil {
		t.Fatal("missing repaired span")
	}
	// 3600 video ticks at 90k = 0.04s = 28800 media ticks.
	if second.Start.Ticks != 28800 {
		t.Fatalf("second start = %d, want 28800", second.Start.Ticks)
	}
	if second.OriginalTFD != 1800 || second.RepairedTFD != 3600 {
		t.Fatalf("span tfdt = %+v", second)
	}
}

func TestTimelineSequenceGapRecorded(t *testing.T) {
	r, off := mediaRate(t)
	tl, err := BuildTimeline(r, DefaultTrackSpecs(), off, []Input{
		frag(t, "a", "f1.m4s", u32(1), 0, 0, 3600, 1920),
		frag(t, "a", "f3.m4s", u32(3), 3600, 1920, 3600, 1920),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(tl.SequenceGaps) != 1 {
		t.Fatalf("gaps = %+v", tl.SequenceGaps)
	}
	if g := tl.SequenceGaps[0]; len(g.Missing) != 1 || g.Missing[0] != 2 {
		t.Fatalf("missing = %v", g.Missing)
	}
	if len(tl.Repairs) != 0 {
		t.Fatalf("gap must not create a repair: %+v", tl.Repairs)
	}
}

func TestTimelineMissingMFHDNotInferred(t *testing.T) {
	r, off := mediaRate(t)
	tl, err := BuildTimeline(r, DefaultTrackSpecs(), off, []Input{
		frag(t, "a", "f1.m4s", u32(1), 0, 0, 3600, 1920),
		frag(t, "a", "f2.m4s", nil, 3600, 1920, 3600, 1920),
		frag(t, "a", "f3.m4s", u32(3), 7200, 3840, 3600, 1920),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(tl.SequenceGaps) != 1 || tl.SequenceGaps[0].Missing[0] != 2 {
		t.Fatalf("gaps = %+v", tl.SequenceGaps)
	}
}

func TestTimelineOffGridFails(t *testing.T) {
	r, off := mediaRate(t)
	specs := TrackSpecs{VideoTrackID: 1, VideoTimescale: 44100, AudioTrackID: 2, AudioTimescale: 48000}
	in := frag(t, "a", "f1.m4s", u32(1), 1, 0, 1, 1920)
	if _, err := BuildTimeline(r, specs, off, []Input{in}); err == nil {
		t.Fatal("expected off-grid error")
	}
}

func TestTimelineUnknownTrackFails(t *testing.T) {
	r, off := mediaRate(t)
	specs := TrackSpecs{VideoTrackID: 7, VideoTimescale: 90000, AudioTrackID: 8, AudioTimescale: 48000}
	in := frag(t, "a", "f1.m4s", u32(1), 0, 0, 3600, 1920)
	if _, err := BuildTimeline(r, specs, off, []Input{in}); err == nil {
		t.Fatal("expected unknown track error")
	}
}

func TestTimelinePerSourceIsolation(t *testing.T) {
	r, off := mediaRate(t)
	off["b"] = 0
	tl, err := BuildTimeline(r, DefaultTrackSpecs(), off, []Input{
		frag(t, "a", "a1.m4s", u32(1), 3600, 1920, 3600, 1920),
		frag(t, "b", "b1.m4s", u32(1), 0, 0, 3600, 1920),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(tl.Repairs) != 0 {
		t.Fatalf("cross-source repair should not happen: %+v", tl.Repairs)
	}
}

func TestTimelineSourceOffsetApplied(t *testing.T) {
	r, off := mediaRate(t)
	off["b"] = 28800 // 0.04s
	tl, err := BuildTimeline(r, DefaultTrackSpecs(), off, []Input{
		frag(t, "a", "a1.m4s", u32(1), 0, 0, 3600, 1920),
		frag(t, "b", "b1.m4s", u32(1), 0, 0, 3600, 1920),
	})
	if err != nil {
		t.Fatal(err)
	}
	var bStart int64 = -1
	for _, ft := range tl.Fragments {
		if ft.Source == "b" {
			bStart = ft.Tracks[0].Start.Ticks
		}
	}
	if bStart != 28800 {
		t.Fatalf("source b start = %d, want 28800", bStart)
	}
}

func TestTimelineMissingSourceOffsetFails(t *testing.T) {
	r, _ := mediaRate(t)
	in := frag(t, "z", "z1.m4s", u32(1), 0, 0, 3600, 1920)
	if _, err := BuildTimeline(r, DefaultTrackSpecs(), map[string]int64{"a": 0}, []Input{in}); err == nil {
		t.Fatal("expected missing offset error")
	}
}
