package container_test

import (
	"errors"
	"testing"

	"mq/internal/container"
	"mq/internal/fixtures"
	"mq/internal/timeline"
)

func asStageErr(t *testing.T, err error) *timeline.Error {
	t.Helper()
	var terr *timeline.Error
	if !errors.As(err, &terr) {
		t.Fatalf("expected *timeline.Error, got %T: %v", err, err)
	}
	return terr
}

func TestParseGoodMP4(t *testing.T) {
	asset, err := container.Parse(fixtures.GoodMP4(), container.ParseOptions{MaxTracks: 8})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if asset.Format != "mp4" || len(asset.Tracks) != 2 {
		t.Fatalf("format=%s tracks=%d", asset.Format, len(asset.Tracks))
	}
	v := asset.Tracks[0]
	if v.Kind != container.KindVideo || v.EditRate.Num != 12288 {
		t.Fatalf("video kind=%s rate=%v", v.Kind, v.EditRate)
	}
	if len(v.Samples) != 24 {
		t.Fatalf("video samples=%d", len(v.Samples))
	}
	if v.Rotation != 90 {
		t.Fatalf("rotation=%d", v.Rotation)
	}
	if v.Origin != 512 {
		t.Fatalf("origin=%d", v.Origin)
	}
	if v.Crop == nil || v.Crop.W != 48 || v.Crop.H != 88 {
		t.Fatalf("crop=%+v", v.Crop)
	}
	if v.Color.Primaries != 1 || v.Color.Transfer != 13 || !v.Color.FullRange {
		t.Fatalf("color=%+v", v.Color)
	}
	// 每项关键字段须有来源偏移。
	for _, f := range []string{"rotation", "crop", "color", "edit_rate", "origin"} {
		if v.OffsetOf(f) < 0 {
			t.Fatalf("missing provenance offset for %s", f)
		}
	}
	a := asset.Tracks[1]
	if a.Kind != container.KindAudio || a.EditRate.Num != 48000 || len(a.Samples) != 25 {
		t.Fatalf("audio kind=%s rate=%v samples=%d", a.Kind, a.EditRate, len(a.Samples))
	}
	// 样本偏移须落在文件内且可切片。
	for _, s := range v.Samples {
		if !container.CheckSpan(int64(len(asset.Raw)), s.Offset, s.Size) {
			t.Fatalf("sample %+v out of file", s)
		}
	}
}

func TestParseGoodMXF(t *testing.T) {
	asset, err := container.Parse(fixtures.GoodMXF(), container.ParseOptions{MaxTracks: 8})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if asset.Format != "mxf" || len(asset.Tracks) != 3 {
		t.Fatalf("format=%s tracks=%d", asset.Format, len(asset.Tracks))
	}
	v := asset.Tracks[0]
	if v.Kind != container.KindVideo || len(v.Samples) != 24 {
		t.Fatalf("video kind=%s samples=%d", v.Kind, len(v.Samples))
	}
	if v.AltColor.Zero() || v.Color.Zero() {
		t.Fatalf("expected conflicting color declarations: %+v / %+v", v.Color, v.AltColor)
	}
	if v.OffsetOf("crop") < 0 || v.OffsetOf("color") < 0 {
		t.Fatalf("missing provenance offsets: %+v", v.Offsets)
	}
	for _, s := range v.Samples {
		if !container.CheckSpan(int64(len(asset.Raw)), s.Offset, s.Size) {
			t.Fatalf("sample %+v out of file", s)
		}
	}
}

func TestTruncatedMP4BoxOffset(t *testing.T) {
	buf, wantOff := fixtures.TruncatedMP4()
	_, err := container.Parse(buf, container.ParseOptions{})
	terr := asStageErr(t, err)
	if terr.Stage != timeline.StageParse {
		t.Fatalf("stage=%s", terr.Stage)
	}
	if terr.Offset != wantOff {
		t.Fatalf("offset=%d want %d", terr.Offset, wantOff)
	}
	if terr.Fatal {
		t.Fatalf("truncated box should be recoverable")
	}
}

func TestIllegalBoxSize(t *testing.T) {
	// 盒长度小于头部长度（4 < 8）为非法长度。
	buf := []byte{0, 0, 0, 4, 'f', 'r', 'e', 'e'}
	_, err := container.ParseMP4(buf)
	terr := asStageErr(t, err)
	if terr.Stage != timeline.StageParse || terr.Offset != 0 {
		t.Fatalf("stage=%s offset=%d", terr.Stage, terr.Offset)
	}
}

func TestMXFOverrunOffset(t *testing.T) {
	buf, wantOff := fixtures.MXFOverrun()
	_, err := container.Parse(buf, container.ParseOptions{})
	terr := asStageErr(t, err)
	if terr.Stage != timeline.StageParse {
		t.Fatalf("stage=%s", terr.Stage)
	}
	if terr.Offset != wantOff {
		t.Fatalf("offset=%d want %d", terr.Offset, wantOff)
	}
}

func TestMXFMissingRequiredField(t *testing.T) {
	// 无编辑率的轨道元数据：未知必需字段，须停止该素材。
	buf := fixtures.MXFMissingEditRate()
	_, err := container.Parse(buf, container.ParseOptions{})
	terr := asStageErr(t, err)
	if terr.Stage != timeline.StageParse {
		t.Fatalf("stage=%s", terr.Stage)
	}
}

func TestUnknownSignatureIsFatal(t *testing.T) {
	_, err := container.Parse(fixtures.Garbage(), container.ParseOptions{})
	terr := asStageErr(t, err)
	if !terr.Fatal {
		t.Fatalf("unknown signature should be fatal: %v", err)
	}
}

func TestTrackLimitIsFatal(t *testing.T) {
	_, err := container.Parse(fixtures.TooManyTracksMP4(9), container.ParseOptions{MaxTracks: 8})
	terr := asStageErr(t, err)
	if !terr.Fatal {
		t.Fatalf("track limit should be fatal: %v", err)
	}
}

func TestInputSizeLimitIsFatal(t *testing.T) {
	_, err := container.Parse(fixtures.GoodMP4(), container.ParseOptions{MaxBytes: 1024})
	terr := asStageErr(t, err)
	if !terr.Fatal {
		t.Fatalf("size limit should be fatal: %v", err)
	}
}
