package maptrack

import (
	"errors"
	"testing"

	"archive/internal/fault"
	"archive/internal/model"
	"archive/internal/probe"
)

func track(logical string, source string, kind model.TrackKind, color model.ColorDescription, deps ...string) *model.Track {
	claims := []model.ColorField{
		{Field: "primaries", Value: color.Primaries.Value, Source: color.Primaries.Source},
		{Field: "transfer", Value: color.Transfer.Value, Source: color.Transfer.Source},
		{Field: "matrix", Value: color.Matrix.Value, Source: color.Matrix.Source},
		{Field: "range", Value: color.Range.Value, Source: color.Range.Source},
	}
	return &model.Track{
		Key: logical + ":" + source, Logical: logical, Source: source,
		Container: model.ContainerKind(source), TrackID: 1, Kind: kind,
		Required: kind == model.KindVideo || kind == model.KindAudio,
		Timebase: model.Timebase{Timescale: 1000}, DurationTick: 1000,
		Color: color, ColorClaims: claims, DependsOn: deps,
	}
}

func color(p, t, m, r, src string) model.ColorDescription {
	return model.ColorDescription{
		Primaries:    model.ColorField{Field: "primaries", Value: p, Source: src},
		Transfer:     model.ColorField{Field: "transfer", Value: t, Source: src},
		Matrix:       model.ColorField{Field: "matrix", Value: m, Source: src},
		Range:        model.ColorField{Field: "range", Value: r, Source: src},
		ArbitratedBy: src,
	}
}

func file(name string, tracks ...*model.Track) *probe.File {
	return &probe.File{Container: model.ContainerMP4, Path: name, Tracks: tracks}
}

func TestMapArbitratesMXFOverMP4(t *testing.T) {
	mxf := &probe.File{Container: model.ContainerMXF, Path: "mxf", Tracks: []*model.Track{
		track("video", "mxf", model.KindVideo, color("bt709", "bt709", "bt709", "limited", "mxf")),
	}}
	mp4 := file("mp4",
		track("video", "mp4", model.KindVideo, color("bt2020", "smpte2084", "bt2020nc", "full", "mp4")),
	)
	res, err := Map([]*probe.File{mxf, mp4}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tracks) != 1 {
		t.Fatalf("tracks = %d", len(res.Tracks))
	}
	mt := res.Tracks[0]
	if mt.Color.Primaries.Value != "bt709" || mt.Color.Primaries.Source != "mxf" {
		t.Fatalf("primaries = %+v", mt.Color.Primaries)
	}
	if mt.Color.ArbitratedBy != "mxf" {
		t.Fatalf("arbitrated by %q", mt.Color.ArbitratedBy)
	}
	// The conflicting MP4 claims must all be kept.
	want := map[string]string{"primaries": "bt2020", "transfer": "smpte2084", "matrix": "bt2020nc", "range": "full"}
	got := map[string]string{}
	for _, c := range mt.Color.Conflicts {
		if c.Source != "mp4" {
			t.Fatalf("conflict from %q", c.Source)
		}
		got[c.Field] = c.Value
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("conflict %s = %q, want %q (all %v)", k, got[k], v, got)
		}
	}
	if len(mt.Sources) != 2 || mt.Sources[0] != "mxf" {
		t.Fatalf("sources = %v", mt.Sources)
	}
}

func TestMapUsesDefaultOnlyWhenNoContainerValue(t *testing.T) {
	f := file("f", track("video", "mp4", model.KindVideo, color("bt709", "bt709", "bt709", "limited", "default")))
	res, err := Map([]*probe.File{f}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Tracks[0].Color.ArbitratedBy != "default" {
		t.Fatalf("arbitrated by %q", res.Tracks[0].Color.ArbitratedBy)
	}
}

func TestMapEmitsInDependencyOrder(t *testing.T) {
	f := file("f",
		track("audio", "mp4", model.KindAudio, color("bt709", "bt709", "bt709", "limited", "default"), "video"),
		track("video", "mp4", model.KindVideo, color("bt709", "bt709", "bt709", "limited", "mp4")),
	)
	res, err := Map([]*probe.File{f}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Order) != 2 || res.Order[0] != "video" || res.Order[1] != "audio" {
		t.Fatalf("order = %v", res.Order)
	}
	if res.Tracks[0].OrderKey != 0 || res.Tracks[1].OrderKey != 1 {
		t.Fatalf("order keys = %d,%d", res.Tracks[0].OrderKey, res.Tracks[1].OrderKey)
	}
	if len(res.Tracks[1].Dependencies) != 1 || res.Tracks[1].Dependencies[0] != "video" {
		t.Fatalf("audio deps = %v", res.Tracks[1].Dependencies)
	}
}

func TestMapDetectsCycle(t *testing.T) {
	f := file("f",
		track("a", "mp4", model.KindData, color("bt709", "bt709", "bt709", "limited", "default"), "b"),
		track("b", "mp4", model.KindData, color("bt709", "bt709", "bt709", "limited", "default"), "a"),
	)
	res, err := Map([]*probe.File{f}, Options{})
	if err == nil || res != nil {
		t.Fatalf("expected cycle error, got %v %v", res, err)
	}
	var fe *fault.Error
	if !errors.As(err, &fe) || fe.Code != fault.DependencyCycle {
		t.Fatalf("error = %v", err)
	}
}

func TestMapMissingRequiredTrackFails(t *testing.T) {
	f := file("f", track("video", "mp4", model.KindVideo, color("bt709", "bt709", "bt709", "limited", "mp4")))
	res, err := Map([]*probe.File{f}, Options{RequireTracks: []string{"audio"}})
	if err == nil || res != nil {
		t.Fatal("expected missing required track")
	}
	var fe *fault.Error
	if !errors.As(err, &fe) || fe.Code != fault.MissingRequiredTrack {
		t.Fatalf("error = %v", err)
	}
}

func TestMapOptionalMetadataMayBeMissing(t *testing.T) {
	tr := track("video", "mp4", model.KindVideo, color("bt709", "bt709", "bt709", "limited", "mp4"))
	tr.MissingOptional = []string{"colr", "clean_aperture"}
	res, err := Map([]*probe.File{file("f", tr)}, Options{})
	if err != nil {
		t.Fatalf("optional absence must not fail: %v", err)
	}
	if len(res.Tracks) != 1 {
		t.Fatalf("tracks = %d", len(res.Tracks))
	}
}

func TestMapKeepsContainerLocalCropAndRotation(t *testing.T) {
	mxf := &probe.File{Container: model.ContainerMXF, Path: "mxf", Tracks: []*model.Track{
		{Key: "mxf/t1", Logical: "video", Source: "mxf", Container: model.ContainerMXF, TrackID: 1,
			Kind: model.KindVideo, Timebase: model.Timebase{Timescale: 1000}, DurationTick: 1000,
			RotationDeg: 90, Crop: &model.Crop{Right: 1920, Bottom: 1080},
			ColorClaims: []model.ColorField{{Field: "primaries", Value: "bt709", Source: "mxf"}}},
	}}
	mp4 := &probe.File{Container: model.ContainerMP4, Path: "mp4", Tracks: []*model.Track{
		{Key: "mp4/t1", Logical: "video", Source: "mp4", Container: model.ContainerMP4, TrackID: 1,
			Kind: model.KindVideo, Timebase: model.Timebase{Timescale: 1000}, DurationTick: 1000,
			RotationDeg: 270, Crop: &model.Crop{Right: 1080, Bottom: 1920},
			ColorClaims: []model.ColorField{{Field: "primaries", Value: "bt2020", Source: "mp4"}}},
	}}
	res, err := Map([]*probe.File{mxf, mp4}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	mt := res.Tracks[0]
	if mt.Rotation != 90 || mt.Crop == nil || mt.Crop.Right != 1920 {
		t.Fatalf("physical metadata = rot %d crop %+v", mt.Rotation, mt.Crop)
	}
}

func TestMapDeterministic(t *testing.T) {
	files := []*probe.File{
		file("b", track("audio", "mp4", model.KindAudio, color("bt709", "bt709", "bt709", "limited", "default"))),
		file("a", track("video", "mp4", model.KindVideo, color("bt2020", "bt709", "bt709", "limited", "mp4"))),
	}
	first, err := Map(files, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		next, err := Map(files, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if len(next.Order) != len(first.Order) {
			t.Fatal("order length changed")
		}
		for j := range first.Order {
			if next.Order[j] != first.Order[j] {
				t.Fatalf("order changed: %v vs %v", next.Order, first.Order)
			}
		}
	}
}
