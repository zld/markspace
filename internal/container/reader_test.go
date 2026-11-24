package container_test

import (
	"errors"
	"testing"

	"github.com/zld/mediarc/internal/container"
	"github.com/zld/mediarc/internal/fixture"
	"github.com/zld/mediarc/internal/model"
)

func TestGeneratedAssetParsesAndRoundtrips(t *testing.T) {
	asset, err := fixture.GenerateDemo(17)
	if err != nil {
		t.Fatal(err)
	}
	f, err := container.ReadBytes(asset.MP4, asset.Config)
	if err != nil {
		t.Fatalf("ReadBytes: %v", err)
	}
	if len(f.Tracks) != 2 {
		t.Fatalf("tracks=%d want 2", len(f.Tracks))
	}
	for _, tr := range f.Tracks {
		if tr.Metadata.Timescale == 0 {
			t.Fatalf("zero timescale on track %d", tr.Metadata.TrackID)
		}
		if len(tr.Events) == 0 {
			t.Fatalf("track %d has no events", tr.Metadata.TrackID)
		}
		var keyframes int
		for _, ev := range tr.Events {
			if ev.Keyframe {
				keyframes++
			}
			if ev.TimestampBits != 32 {
				t.Fatalf("bits=%d", ev.TimestampBits)
			}
		}
		if keyframes < 4 {
			t.Fatalf("track %d keyframes=%d", tr.Metadata.TrackID, keyframes)
		}
	}
	report, err := container.VerifyRoundtrip(f)
	if err != nil {
		t.Fatal(err)
	}
	if report.Rate != 1 {
		t.Fatalf("roundtrip rate=%f mismatches=%+v", report.Rate, report.Boxes)
	}
}

func TestTruncatedBoxReportsOffset(t *testing.T) {
	asset, err := fixture.TruncatedBoxAsset(17)
	if err != nil {
		t.Fatal(err)
	}
	_, err = container.ReadBytes(asset.MP4, asset.Config)
	if err == nil {
		t.Fatal("expected truncation error")
	}
	offset, ok := model.ByteOffset(err)
	if !ok {
		t.Fatalf("error has no byte offset: %T %v", err, err)
	}
	var pe *model.ParseError
	if !errors.As(err, &pe) || pe.TrackID != 1 {
		t.Fatalf("error track context missing: %v", err)
	}
	if offset != asset.CorruptOffset {
		t.Fatalf("offset=%d want %d: %v", offset, asset.CorruptOffset, err)
	}
}

func TestZeroTimescaleErrorHasTrackAndOffset(t *testing.T) {
	asset, offset, err := fixture.ZeroTimescaleAsset(17)
	if err != nil {
		t.Fatal(err)
	}
	_, err = container.ReadBytes(asset.MP4, asset.Config)
	if err == nil {
		t.Fatal("expected zero timescale error")
	}
	var pe *model.ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("error type %T: %v", err, err)
	}
	if pe.TrackID != 1 || pe.ByteOffset != offset || pe.ByteOffset <= 0 {
		t.Fatalf("parse error track/offset mismatch: %+v want offset %d", pe, offset)
	}
	if !errors.Is(err, model.ErrZeroTimescale) {
		t.Fatalf("err=%v does not wrap zero timescale", err)
	}
	if _, err := model.ToNS(1, 0); err != model.ErrZeroTimescale {
		t.Fatalf("direct conversion err=%v", err)
	}
}
