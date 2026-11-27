package maptrack

import (
	"testing"

	"mediapack/internal/demux"
	"mediapack/internal/model"
)

func containersForAdjudication() []*demux.Container {
	mxf := &demux.Container{
		Path: "a.mxf", Format: "mxf",
		Tracks: []model.TrackMeta{
			{
				TrackID: 1, Timescale: 25, Codec: "vraw", Handler: "vide",
				Rotation: model.Field[model.Rotation]{
					Value: model.Rotation{Degrees: 90}, Source: model.SourceMXF, Raw: "90"},
				Color: model.Field[model.Color]{
					Value: model.Color{Primaries: 9}, Source: model.SourceMXF, Raw: "9"},
			},
		},
		Samples: map[uint32][]model.Sample{
			1: {{Source: "a.mxf", SourceTrack: 1, Index: 0, Size: 4}},
		},
	}
	mp4 := &demux.Container{
		Path: "b.mp4", Format: "mp4",
		Tracks: []model.TrackMeta{
			{
				TrackID: 1, Timescale: 24000, Codec: "avc1", Handler: "vide",
				Rotation: model.Field[model.Rotation]{
					Value: model.Rotation{Degrees: 180}, Source: model.SourceMP4, Raw: "180"},
				Crop: model.Field[model.Crop]{
					Value: model.Crop{Left: 8}, Source: model.SourceMP4, Raw: "8"},
				Color: model.Field[model.Color]{
					Value: model.Color{Primaries: 1}, Source: model.SourceMP4, Raw: "1"},
			},
		},
		Samples: map[uint32][]model.Sample{
			1: {{Source: "b.mp4", SourceTrack: 1, Index: 0, Size: 6}},
		},
	}
	return []*demux.Container{mxf, mp4}
}

func TestBuildHappyAndRelabel(t *testing.T) {
	cs := containersForAdjudication()
	rules := []Rule{{SourcePath: "a.mxf", SourceTrack: 1, TargetTrack: 10, Role: "vide"}}
	m, err := Build(cs, rules, StandardDefaults())
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Bindings) != 1 {
		t.Fatalf("bindings %d", len(m.Bindings))
	}
	b := m.Bindings[0]
	if b.TargetTrack != 10 || b.SourceTrack != 1 || b.Format != "mxf" {
		t.Fatalf("binding wrong: %+v", b)
	}
	if b.Samples[0].TargetTrack != 10 || b.Samples[0].Source != "a.mxf" {
		t.Fatalf("sample not relabeled: %+v", b.Samples[0])
	}
}

func TestMissingTrackRejected(t *testing.T) {
	cs := containersForAdjudication()
	rules := []Rule{{SourcePath: "a.mxf", SourceTrack: 99, TargetTrack: 1}}
	_, err := Build(cs, rules, StandardDefaults())
	me, ok := model.AsMediaError(err)
	if !ok || me.Code != model.CodeMissingTrack || me.Track != 99 {
		t.Fatalf("want missing-track 99, got %v", err)
	}
}

func TestDuplicateTargetRejected(t *testing.T) {
	cs := containersForAdjudication()
	// Add a second distinct source so both rules resolve before the clash.
	cs[0].Tracks = append(cs[0].Tracks, model.TrackMeta{
		TrackID: 2, Codec: "pcm ", Handler: "soun",
	})
	cs[0].Samples[2] = []model.Sample{{Source: "a.mxf", SourceTrack: 2}}
	rules := []Rule{
		{SourcePath: "a.mxf", SourceTrack: 1, TargetTrack: 7},
		{SourcePath: "a.mxf", SourceTrack: 2, TargetTrack: 7},
	}
	_, err := Build(cs, rules, StandardDefaults())
	me, ok := model.AsMediaError(err)
	if !ok || me.Code != model.CodeDuplicateTarget || me.Track != 7 {
		t.Fatalf("want duplicate-target 7, got %v", err)
	}
}

func TestIncompatibleRoleRejected(t *testing.T) {
	cs := containersForAdjudication()
	rules := []Rule{{SourcePath: "a.mxf", SourceTrack: 1, TargetTrack: 1, Role: "soun"}}
	_, err := Build(cs, rules, StandardDefaults())
	me, ok := model.AsMediaError(err)
	if !ok || me.Code != model.CodeIncompatibleCodec || me.Track != 1 {
		t.Fatalf("want incompatible-codec on track 1, got %v", err)
	}
}

func TestAdjudicationPrecedenceAndDefaults(t *testing.T) {
	cs := containersForAdjudication()
	rules := []Rule{{SourcePath: "b.mp4", SourceTrack: 1, TargetTrack: 1}}
	m, err := Build(cs, rules, StandardDefaults())
	if err != nil {
		t.Fatal(err)
	}
	b := m.Bindings[0]
	// Both containers carry track 1: MXF must win rotation and color even
	// though the binding source is the MP4.
	if b.Meta.Rotation.Value.Degrees != 90 || b.Meta.Rotation.Source != model.SourceMXF {
		t.Fatalf("rotation adjudication wrong: %+v", b.Meta.Rotation)
	}
	if b.Meta.Color.Value.Primaries != 9 || b.Meta.Color.Source != model.SourceMXF {
		t.Fatalf("color adjudication wrong: %+v", b.Meta.Color)
	}
	// Crop exists only on MP4, so MP4 is the adopted source for that field.
	if b.Meta.Crop.Value.Left != 8 || b.Meta.Crop.Source != model.SourceMP4 {
		t.Fatalf("crop adjudication wrong: %+v", b.Meta.Crop)
	}
	// Every decision retains candidates including a default, and rotation's
	// candidate list shows the MP4 original (180) even though MXF won.
	for _, d := range b.Decisions {
		if len(d.Candidates) == 0 || d.Adopted.Source == "" {
			t.Fatalf("decision %s missing evidence", d.Field)
		}
	}
	rot := findDecision(b.Decisions, "rotation")
	if !hasRaw(rot, "180") {
		t.Fatalf("rotation decision lost MP4 original: %+v", rot.Candidates)
	}
}

func TestDefaultFallbackWhenNoContainerField(t *testing.T) {
	c := &demux.Container{
		Path: "x.mp4", Format: "mp4",
		Tracks: []model.TrackMeta{{TrackID: 3, Codec: "avc1", Handler: "vide"}},
		Samples: map[uint32][]model.Sample{
			3: {{Source: "x.mp4", SourceTrack: 3}},
		},
	}
	m, err := Build([]*demux.Container{c},
		[]Rule{{SourcePath: "x.mp4", SourceTrack: 3, TargetTrack: 3}},
		StandardDefaults())
	if err != nil {
		t.Fatal(err)
	}
	b := m.Bindings[0]
	if b.Meta.Rotation.Source != model.SourceDefault ||
		b.Meta.Rotation.Value.Degrees != 0 {
		t.Fatalf("expected default rotation, got %+v", b.Meta.Rotation)
	}
	if b.Meta.Color.Source != model.SourceDefault ||
		b.Meta.Color.Value.Primaries != 1 {
		t.Fatalf("expected default color, got %+v", b.Meta.Color)
	}
}

func findDecision(ds []Decision, field string) Decision {
	for _, d := range ds {
		if d.Field == field {
			return d
		}
	}
	return Decision{}
}

func hasRaw(d Decision, raw string) bool {
	for _, f := range d.Candidates {
		if f.Raw == raw {
			return true
		}
	}
	return false
}
