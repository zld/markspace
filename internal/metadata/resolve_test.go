package metadata_test

import (
	"testing"

	"mediaprep/internal/metadata"
	"mediaprep/internal/model"
	"mediaprep/internal/source"
)

func TestResolvePriorityRetainsAllProvenance(t *testing.T) {
	tr := &source.Track{Label: "mp4:1", Width: 320, Height: 180, RawFields: []source.RawField{
		{Name: "color", Value: "container", Scope: source.ScopeContainer, Offset: 30},
		{Name: "color", Value: "track", Scope: source.ScopeTrack, Offset: 20},
		{Name: "color", Value: "sample", Scope: source.ScopeSampleOrDescriptor, Offset: 10},
	}}
	got, err := metadata.ResolveTrack(tr)
	if err != nil {
		t.Fatal(err)
	}
	f := got.Fields["color"]
	if !f.Conflict || f.Value != "sample" || f.Scope != source.ScopeSampleOrDescriptor {
		t.Fatalf("winner=%+v", f)
	}
	if len(f.Alternatives) != 3 || len(got.Conflicts) != 1 {
		t.Fatalf("provenance=%d conflicts=%d", len(f.Alternatives), len(got.Conflicts))
	}
}

func TestResolveRejectsInvalidRotationAndCrop(t *testing.T) {
	_, err := metadata.ResolveTrack(&source.Track{RawFields: []source.RawField{
		{Name: "rotation", Value: "45", Scope: source.ScopeSampleOrDescriptor, Offset: 77},
	}})
	pe, ok := model.AsError(err)
	if !ok || pe.Code != model.CodeInvalidMetadata || !pe.HasOffset {
		t.Fatalf("rotation error=%v", err)
	}

	_, err = metadata.ResolveTrack(&source.Track{
		Width: 100, Height: 100,
		RawFields: []source.RawField{{Name: "crop", Value: "90,90,20,20", Scope: source.ScopeTrack, Offset: 88}},
	})
	pe, ok = model.AsError(err)
	if !ok || pe.Code != model.CodeInvalidMetadata || pe.Offset != 88 {
		t.Fatalf("crop error=%v", err)
	}
}
