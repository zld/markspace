package evidence_test

import (
	"errors"
	"testing"

	"mq/internal/container"
	"mq/internal/evidence"
	"mq/internal/fixtures"
	"mq/internal/timeline"
)

func composedMP4(t *testing.T) (*timeline.Timeline, *timeline.Asset) {
	t.Helper()
	asset, err := container.Parse(fixtures.GoodMP4(), container.ParseOptions{MaxTracks: 8})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	tl, err := timeline.Compose(asset, timeline.ComposeOptions{})
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	return tl, asset
}

func TestExportFramesDeterministic(t *testing.T) {
	tl, asset := composedMP4(t)
	a, err := evidence.ExportFrames(tl, asset)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	b, err := evidence.ExportFrames(tl, asset)
	if err != nil {
		t.Fatalf("re-export: %v", err)
	}
	if len(a) != 24 || len(b) != 24 {
		t.Fatalf("frames=%d/%d", len(a), len(b))
	}
	for i := range a {
		if a[i].Hash != b[i].Hash {
			t.Fatalf("frame %d hash differs across runs", i)
		}
	}
	if evidence.FramesHash(a) != evidence.FramesHash(b) {
		t.Fatalf("aggregate hash differs across runs")
	}
	// 帧摘要须覆盖时间、尺寸与像素统计。
	f := a[0]
	if f.Width != 48 || f.Height != 88 || f.Mean <= 0 || f.Max < f.Min {
		t.Fatalf("bad frame summary: %+v", f)
	}
}

func TestAlignmentReport(t *testing.T) {
	mk := func(units int64) timeline.Time { return timeline.Time{Units: units, Scale: 90000} }
	frames := []evidence.FrameSummary{
		{Index: 0, Time: mk(0)},
		{Index: 1, Time: mk(3750)},
		{Index: 2, Time: mk(7500)},
	}
	entries := []evidence.Entry{
		{Index: 0, TimelineTime: mk(0)},
		{Index: 1, TimelineTime: mk(3795)}, // +0.5ms
		{Index: 2, TimelineTime: mk(7410)}, // -1.0ms
	}
	rep, err := evidence.CheckAlignment(frames, entries)
	if err != nil {
		t.Fatalf("align: %v", err)
	}
	if rep.MaxMs < 0.99 || rep.MaxMs > 1.01 {
		t.Fatalf("max=%v want ~1.0", rep.MaxMs)
	}
	if rep.MeanMs < 0.49 || rep.MeanMs > 0.51 {
		t.Fatalf("mean=%v want ~0.5", rep.MeanMs)
	}
	// 缺帧：条目数不一致须报错。
	if _, err := evidence.CheckAlignment(frames, entries[:2]); err == nil {
		t.Fatalf("expected missing-frames error")
	}
}

func TestBitrateDelta(t *testing.T) {
	rep := evidence.CheckBitrate(1_000_000, timeline.NewRat(1, 1), 8_000_000)
	if rep.ActualBps != 8_000_000 || rep.DeltaPct != 0 {
		t.Fatalf("actual=%d delta=%v", rep.ActualBps, rep.DeltaPct)
	}
	rep = evidence.CheckBitrate(1_050_000, timeline.NewRat(1, 1), 8_000_000)
	if rep.DeltaPct < 4.9 || rep.DeltaPct > 5.1 {
		t.Fatalf("delta=%v want ~5.0", rep.DeltaPct)
	}
}

func TestCheckFramesLocatingErrors(t *testing.T) {
	mk := func(i int, units int64) evidence.FrameSummary {
		return evidence.FrameSummary{Index: i, Time: timeline.Time{Units: units, Scale: 90000}}
	}
	// 缺帧定位。
	err := evidence.CheckFrames([]evidence.FrameSummary{mk(0, 0), mk(2, 3750)})
	var terr *timeline.Error
	if !errors.As(err, &terr) || terr.Stage != timeline.StageValidate {
		t.Fatalf("expected validate error, got %v", err)
	}
	// 重复时间戳定位。
	err = evidence.CheckFrames([]evidence.FrameSummary{mk(0, 0), mk(1, 3750), mk(2, 3750)})
	if !errors.As(err, &terr) || terr.Stage != timeline.StageValidate {
		t.Fatalf("expected duplicate timestamp error, got %v", err)
	}
	// 正常序列通过。
	if err := evidence.CheckFrames([]evidence.FrameSummary{mk(0, 0), mk(1, 3750)}); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestCheckTransformCropOOB(t *testing.T) {
	tl := &timeline.Timeline{Tracks: []timeline.ComposedTrack{{
		Meta: timeline.TrackMeta{
			Name: "v", Kind: timeline.KindVideo, Width: 96, Height: 54,
			Offsets: map[string]int64{"crop": 4242},
		},
		Transform: timeline.Transform{
			RotWidth: 96, RotHeight: 54, Width: 200, Height: 200,
			Crop: &timeline.CropRect{X: 0, Y: 0, W: 200, H: 200},
		},
	}}}
	err := evidence.CheckTransform(tl)
	var terr *timeline.Error
	if !errors.As(err, &terr) {
		t.Fatalf("expected validate error, got %v", err)
	}
	if terr.Stage != timeline.StageValidate || terr.Offset != 4242 {
		t.Fatalf("stage=%s offset=%d", terr.Stage, terr.Offset)
	}
}

func TestQualityReport(t *testing.T) {
	tl, asset := composedMP4(t)
	frames, err := evidence.ExportFrames(tl, asset)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	q := evidence.CheckQuality(frames)
	if q.Mean <= 0 || q.Mean > 1 || q.Min <= 0 || q.Min > q.Mean {
		t.Fatalf("quality=%+v", q)
	}
}
