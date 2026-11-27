package ingest

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"regress/internal/fixture"
	"regress/internal/model"
)

func writeFixtures(t *testing.T, dir string, fault fixture.Fault) (mp4Path, rtpPath string, spec fixture.Spec, plan []model.PlanItem) {
	t.Helper()
	spec = fixture.DefaultSpec(fault)
	var err error
	plan, err = fixture.Plan(spec)
	if err != nil {
		t.Fatal(err)
	}
	mp4Bytes, cut, err := fixture.BuildMP4(spec, plan)
	if err != nil {
		t.Fatal(err)
	}
	cap, err := fixture.BuildRTP(spec, plan)
	if err != nil {
		t.Fatal(err)
	}
	rtpBytes, err := cap.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if fault == fixture.FaultTruncate {
		mp4Bytes = mp4Bytes[:cut]
		rtpBytes = rtpBytes[:cap.TruncatedByte]
	}
	mp4Path = filepath.Join(dir, "capture.mp4")
	rtpPath = filepath.Join(dir, "capture.rtp")
	if err := os.WriteFile(mp4Path, mp4Bytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rtpPath, rtpBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return mp4Path, rtpPath, spec, plan
}

func TestReadMP4Normal(t *testing.T) {
	dir := t.TempDir()
	mp4Path, _, _, _ := writeFixtures(t, dir, fixture.FaultNone)
	res, err := ReadMP4(mp4Path)
	if err != nil {
		t.Fatalf("ReadMP4: %v", err)
	}
	if len(res.Tracks) != len(fixture.AllTrackSpecs()) {
		t.Fatalf("tracks = %d, want %d", len(res.Tracks), len(fixture.AllTrackSpecs()))
	}
	// Video rendition ticks must be exact multiples of the frame interval
	// and every sample must carry rendered frame data.
	var videoTracks int
	for _, tr := range res.Tracks {
		if tr.Kind != model.KindVideo {
			continue
		}
		videoTracks++
		if tr.Timescale != fixture.VideoHz {
			t.Errorf("%s timescale = %d", tr.ID, tr.Timescale)
		}
		if len(tr.Samples) == 0 {
			t.Fatalf("%s has no samples", tr.ID)
		}
		for i, s := range tr.Samples {
			if int64(s.Tick) != int64(i)*int64(fixture.VideoFrameTick) {
				t.Fatalf("%s sample %d tick = %d", tr.ID, i, s.Tick)
			}
			if len(s.Frame) != fixture.FrameW*fixture.FrameH {
				t.Fatalf("%s sample %d missing frame pixels", tr.ID, i)
			}
		}
	}
	if videoTracks != 2 {
		t.Fatalf("want 2 video renditions, got %d", videoTracks)
	}
	// Genuine emsg boxes must be present at moov level.
	if len(res.EmsgEvents) == 0 {
		t.Fatal("no emsg boxes parsed")
	}
	// The dropped first ID3 event must be absent; duplicate emsg present.
	for _, tr := range res.Tracks {
		if tr.Kind == model.KindID3 {
			for _, ev := range tr.Events {
				if ev.Key == fixture.MetaID3+"/evt-000" {
					t.Fatal("dropped ID3 event was reconstructed")
				}
			}
		}
	}
}

func TestReadMP4TruncationReportsOffset(t *testing.T) {
	dir := t.TempDir()
	mp4Path, _, _, _ := writeFixtures(t, dir, fixture.FaultTruncate)
	_, err := ReadMP4(mp4Path)
	if err == nil {
		t.Fatal("expected truncation error, got nil")
	}
	me := model.AsError(err)
	if me == nil {
		t.Fatalf("want *model.Error, got %T: %v", err, err)
	}
	if !errors.Is(err, model.ErrTruncated) {
		t.Fatalf("err = %v, want ErrTruncated", err)
	}
	if me.Stage != model.StageParse || me.Offset <= 0 || me.File != mp4Path {
		t.Fatalf("locatable fields missing: %+v", me)
	}
	t.Logf("truncation located: %s", me.Error())
}

func TestReadRTPNormalUnwraps(t *testing.T) {
	dir := t.TempDir()
	_, rtpPath, spec, _ := writeFixtures(t, dir, fixture.FaultNone)
	res, err := ReadRTP(rtpPath)
	if err != nil {
		t.Fatalf("ReadRTP: %v", err)
	}
	if len(res.Streams) != 4 {
		t.Fatalf("streams = %d, want 4", len(res.Streams))
	}
	// High rendition: 24 samples, strictly increasing ticks across the
	// 32-bit wrap, spaced by exactly one frame interval.
	var high *Stream
	for _, st := range res.Streams {
		if st.TrackID == fixture.RendHigh {
			high = st
		}
	}
	if high == nil || len(high.Samples) != spec.VideoFrames {
		t.Fatalf("high stream samples = %v", high)
	}
	for i := 1; i < len(high.Samples); i++ {
		delta := high.Samples[i].Tick - high.Samples[i-1].Tick
		if delta != fixture.VideoFrameTick {
			t.Fatalf("sample %d delta = %d, want %d", i, delta, fixture.VideoFrameTick)
		}
	}
	// Metadata stream holds events of every tag kind, including duplicate.
	var meta *Stream
	for _, st := range res.Streams {
		if st.SSRC == fixture.SSRCMet {
			meta = st
		}
	}
	kinds := map[model.TrackKind]int{}
	for _, ev := range meta.Events {
		kinds[ev.Kind]++
	}
	for _, k := range []model.TrackKind{model.KindID3, model.KindEmsg, model.KindSEI,
		model.KindChapter, model.KindSubtitle} {
		if kinds[k] == 0 {
			t.Errorf("meta stream missing kind %s", k)
		}
	}
	if kinds[model.KindEmsg] < 2 {
		t.Errorf("duplicate emsg not visible: counts=%v", kinds)
	}
}

func TestReadRTPTruncationReportsOffset(t *testing.T) {
	dir := t.TempDir()
	_, rtpPath, _, _ := writeFixtures(t, dir, fixture.FaultTruncate)
	_, err := ReadRTP(rtpPath)
	if err == nil {
		t.Fatal("expected truncation error, got nil")
	}
	me := model.AsError(err)
	if me == nil || !errors.Is(err, model.ErrTruncated) {
		t.Fatalf("want locatable ErrTruncated, got %v", err)
	}
	if me.Offset <= 0 || me.Stage != model.StageParse {
		t.Fatalf("bad locatable error: %+v", me)
	}
}

func TestReadRTPMissingFile(t *testing.T) {
	_, err := ReadRTP(filepath.Join(t.TempDir(), "nope.rtp"))
	me := model.AsError(err)
	if me == nil || me.Stage != model.StageReceive {
		t.Fatalf("want receive-stage error, got %v", err)
	}
}
