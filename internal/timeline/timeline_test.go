package timeline_test

import (
	"errors"
	"reflect"
	"testing"

	"mq/internal/container"
	"mq/internal/fixtures"
	"mq/internal/timeline"
)

func parse(t *testing.T, buf []byte) *timeline.Asset {
	t.Helper()
	asset, err := container.Parse(buf, container.ParseOptions{MaxTracks: 8})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return asset
}

func TestConvertRoundingAndMonotonic(t *testing.T) {
	cases := []struct {
		units, from, to, want int64
	}{
		{1, 24, 90000, 3750},
		{512, 12288, 90000, 3750},
		{1920, 48000, 90000, 3600},
		{1, 3, 2, 1}, // 0.667 -> 1
		{1, 4, 2, 1}, // 0.5 -> 半数远离零 -> 1
		{-1, 4, 2, -1},
		{3, 8, 2, 1}, // 0.75 -> 1
	}
	for _, c := range cases {
		if got := timeline.Convert(c.units, c.from, c.to); got != c.want {
			t.Fatalf("Convert(%d,%d,%d)=%d want %d", c.units, c.from, c.to, got, c.want)
		}
	}
	// 单调输入换算后不得倒退。
	prev := int64(-1)
	for u := int64(0); u < 100000; u += 7 {
		v := timeline.Convert(u, 44100, 90000)
		if v < prev {
			t.Fatalf("regression at %d: %d < %d", u, v, prev)
		}
		prev = v
	}
}

func TestComposeRerunConsistency(t *testing.T) {
	for _, buf := range [][]byte{fixtures.GoodMXF(), fixtures.GoodMP4()} {
		asset := parse(t, buf)
		a, err := timeline.Compose(asset, timeline.ComposeOptions{})
		if err != nil {
			t.Fatalf("compose: %v", err)
		}
		b, err := timeline.Compose(asset, timeline.ComposeOptions{})
		if err != nil {
			t.Fatalf("recompose: %v", err)
		}
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("compose not deterministic")
		}
	}
}

func TestComposeNonZeroOrigin(t *testing.T) {
	asset := parse(t, fixtures.GoodMP4())
	tl, err := timeline.Compose(asset, timeline.ComposeOptions{})
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	v := tl.Tracks[0]
	if len(v.Samples) == 0 || v.Samples[0].Time.Units != 3750 {
		t.Fatalf("first sample time=%v want 3750/90000", v.Samples[0].Time)
	}
	// 输出时间单调不倒退。
	for i := 1; i < len(v.Samples); i++ {
		if v.Samples[i].Time.Units < v.Samples[i-1].Time.Units {
			t.Fatalf("time regression at %d", i)
		}
	}
}

func TestComposeRotationThenCrop(t *testing.T) {
	asset := parse(t, fixtures.GoodMXF())
	tl, err := timeline.Compose(asset, timeline.ComposeOptions{})
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	tr := tl.Tracks[0].Transform
	if tr.Rotation != 90 {
		t.Fatalf("rotation=%d", tr.Rotation)
	}
	// 先旋转（96x54 -> 54x96）后裁剪（48x88）。
	if tr.RotWidth != 54 || tr.RotHeight != 96 {
		t.Fatalf("rotated dims=%dx%d", tr.RotWidth, tr.RotHeight)
	}
	if tr.Width != 48 || tr.Height != 88 {
		t.Fatalf("final dims=%dx%d", tr.Width, tr.Height)
	}
}

func TestCropOOBTimelineError(t *testing.T) {
	asset := parse(t, fixtures.CropOOBMP4())
	_, err := timeline.Compose(asset, timeline.ComposeOptions{})
	var terr *timeline.Error
	if !errors.As(err, &terr) {
		t.Fatalf("expected timeline.Error, got %v", err)
	}
	if terr.Stage != timeline.StageTimeline {
		t.Fatalf("stage=%s", terr.Stage)
	}
	want := asset.Tracks[0].OffsetOf("crop")
	if terr.Offset != want {
		t.Fatalf("offset=%d want %d", terr.Offset, want)
	}
}

func TestColorConflictDeterministic(t *testing.T) {
	asset := parse(t, fixtures.GoodMXF())
	tl, err := timeline.Compose(asset, timeline.ComposeOptions{})
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	v := tl.Tracks[0]
	// 默认优先级 mxf_track 高于 mxf_descriptor。
	if v.Color.Source != "mxf_track" || v.Color.Primaries != 9 {
		t.Fatalf("winner=%s primaries=%d", v.Color.Source, v.Color.Primaries)
	}
	if len(tl.Decisions) == 0 || tl.Decisions[0].Winner != "mxf_track" {
		t.Fatalf("decisions=%+v", tl.Decisions)
	}
	// 可配置优先级反转后，描述符胜出。
	tl2, err := timeline.Compose(asset, timeline.ComposeOptions{
		ColorPriority: []string{"mxf_descriptor", "mxf_track"},
	})
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	if tl2.Tracks[0].Color.Source != "mxf_descriptor" || tl2.Tracks[0].Color.Primaries != 1 {
		t.Fatalf("winner=%s", tl2.Tracks[0].Color.Source)
	}
}

func TestComposeNegativeDurationRegression(t *testing.T) {
	asset := &timeline.Asset{Format: "mxf", Tracks: []timeline.TrackMeta{{
		ID: 1, Name: "bad", Kind: timeline.KindVideo,
		EditRate: timeline.NewRat(24, 1), Width: 16, Height: 16,
		Samples: []timeline.SampleMeta{
			{Index: 0, Duration: 1},
			{Index: 1, Duration: -5},
		},
	}}}
	_, err := timeline.Compose(asset, timeline.ComposeOptions{})
	var terr *timeline.Error
	if !errors.As(err, &terr) || terr.Stage != timeline.StageTimeline {
		t.Fatalf("expected timeline stage error, got %v", err)
	}
}

func TestComposeEmptyAsset(t *testing.T) {
	tl, err := timeline.Compose(&timeline.Asset{}, timeline.ComposeOptions{})
	if err != nil {
		t.Fatalf("compose empty: %v", err)
	}
	if len(tl.Tracks) != 0 || tl.DurationRat().Num != 0 {
		t.Fatalf("expected empty timeline")
	}
}

func TestResolveColorFallbackDefault(t *testing.T) {
	c, decs := timeline.ResolveColor("t", []timeline.ColorInfo{{Source: "x"}}, nil)
	if c.Source != "default" || len(decs) != 0 {
		t.Fatalf("color=%+v decs=%+v", c, decs)
	}
}
