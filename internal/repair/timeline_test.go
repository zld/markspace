package repair

import (
	"testing"

	"rtx/internal/cmaf"
	"rtx/internal/model"
)

func mediaRate(t *testing.T) model.Rate {
	t.Helper()
	r, err := model.NewRateRatio(720000, 1)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func frag(t *testing.T, source, path string, seq *uint32, videoTFD, audioTFD uint64, videoDur, audioDur uint32) *cmaf.Fragment {
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
	return f
}

func u32(v uint32) *uint32 { return &v }

func TestTimelineCleanIsUnchanged(t *testing.T) {
	tl, err := BuildTimeline(mediaRate(t), DefaultTrackSpecs(), []*cmaf.Fragment{
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
	// Second fragment's video tfdt rewinds to 1800 instead of 3600.
	tl, err := BuildTimeline(mediaRate(t), DefaultTrackSpecs(), []*cmaf.Fragment{
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
	r := tl.Repairs[0]
	if r.Original != 1800 || r.PreviousEnd != 3600 || r.TrackID != 1 {
		t.Fatalf("repair = %+v", r)
	}
	// Repaired value is the previous end (3600 video ticks = 28800 media ticks).
	if r.Repaired != 3600 {
		t.Fatalf("repaired = %d, want 3600", r.Repaired)
	}
	if r.ByteOffset <= 0 {
		t.Fatalf("byte offset = %d", r.ByteOffset)
	}
	if r.Source != "a" || r.Fragment != "f2.m4s" || r.Reason != "tfdt_rollback" {
		t.Fatalf("repair attribution = %+v", r)
	}
	// The second fragment must start at the end of the first on the media grid.
	// 3600 video ticks at 90k = 0.04s = 28800 media ticks.
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
	if second.Start.Ticks != 28800 {
		t.Fatalf("second start = %d, want 28800", second.Start.Ticks)
	}
	if second.OriginalTFD != 1800 || second.RepairedTFD != 3600 {
		t.Fatalf("span tfdt = %+v", second)
	}
}

func TestTimelineSequenceGapRecorded(t *testing.T) {
	tl, err := BuildTimeline(mediaRate(t), DefaultTrackSpecs(), []*cmaf.Fragment{
		frag(t, "a", "f1.m4s", u32(1), 0, 0, 3600, 1920),
		frag(t, "a", "f3.m4s", u32(3), 3600, 1920, 3600, 1920),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(tl.SequenceGaps) != 1 {
		t.Fatalf("gaps = %+v", tl.SequenceGaps)
	}
	g := tl.SequenceGaps[0]
	if len(g.Missing) != 1 || g.Missing[0] != 2 {
		t.Fatalf("missing = %v", g.Missing)
	}
	// Crucially, the missing fragment contributes no duration: the timeline
	// simply continues at the next present fragment's tfdt.
	if len(tl.Repairs) != 0 {
		t.Fatalf("gap must not create a repair: %+v", tl.Repairs)
	}
}

func TestTimelineMissingMFHDNotInferred(t *testing.T) {
	tl, err := BuildTimeline(mediaRate(t), DefaultTrackSpecs(), []*cmaf.Fragment{
		frag(t, "a", "f1.m4s", u32(1), 0, 0, 3600, 1920),
		frag(t, "a", "f2.m4s", nil, 3600, 1920, 3600, 1920),
		frag(t, "a", "f3.m4s", u32(3), 7200, 3840, 3600, 1920),
	})
	if err != nil {
		t.Fatal(err)
	}
	// mfhd 1 -> 3 crosses a missing-2 gap; the gap must still be recorded
	// because the missing fragment did not reset the known sequence.
	if len(tl.SequenceGaps) != 1 || tl.SequenceGaps[0].Missing[0] != 2 {
		t.Fatalf("gaps = %+v", tl.SequenceGaps)
	}
}

func TestTimelineOffGridFails(t *testing.T) {
	// 1 video tick at 90kHz is not a whole number of 720000 ticks... it is
	// (720000/90000 = 8), so use a timescale that does not divide.
	specs := TrackSpecs{VideoTrackID: 1, VideoTimescale: 44100, AudioTrackID: 2, AudioTimescale: 48000}
	f := frag(t, "a", "f1.m4s", u32(1), 1, 0, 1, 1920)
	if _, err := BuildTimeline(mediaRate(t), specs, []*cmaf.Fragment{f}); err == nil {
		t.Fatal("expected off-grid error")
	}
}

func TestTimelineUnknownTrackFails(t *testing.T) {
	specs := TrackSpecs{VideoTrackID: 7, VideoTimescale: 90000, AudioTrackID: 8, AudioTimescale: 48000}
	f := frag(t, "a", "f1.m4s", u32(1), 0, 0, 3600, 1920)
	if _, err := BuildTimeline(mediaRate(t), specs, []*cmaf.Fragment{f}); err == nil {
		t.Fatal("expected unknown track error")
	}
}

func TestTimelinePerSourceIsolation(t *testing.T) {
	// Two sources each start at tfdt 0; that is not a rollback because track
	// state is per source.
	tl, err := BuildTimeline(mediaRate(t), DefaultTrackSpecs(), []*cmaf.Fragment{
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
