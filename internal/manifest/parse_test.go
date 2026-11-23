package manifest

import (
	"errors"
	"strings"
	"testing"

	"regional.example/mediad/internal/fault"
)

func valid() *Asset {
	return &Asset{
		Version:        1,
		AssetID:        "asset-a",
		MovieTimescale: 48000,
		StartTick:      96000,
		Tracks: []TrackDecl{
			{ID: 1, Role: RoleAudio, Codec: CodecPCM16, Required: true, Path: "a.mp4", SampleRate: 48000, Channels: 2, Layout: "stereo", MediaTimescale: 48000},
		},
	}
}

func TestParseValidManifest(t *testing.T) {
	a, err := Parse(strings.NewReader(`{"version":1,"asset_id":"x","movie_timescale":48000,"start_tick":0,"tracks":[{"id":1,"role":"audio","codec":"pcm_s16le","required":true,"depends_on":[],"path":"a.mp4","sample_rate":48000,"channels":2,"layout":"stereo","media_timescale":48000,"edits":[],"sparse_keyframes":false}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if a.Tracks[0].Layout != "stereo" {
		t.Fatalf("got %+v", a.Tracks[0])
	}
}

func TestParseRejectsUnknownField(t *testing.T) {
	_, err := Parse(strings.NewReader(`{"version":1,"asset_id":"x","movie_timescale":48000,"tracks":[],"bogus":1}`))
	if !errors.Is(err, fault.OfClass(fault.ClassManifest, "parse")) {
		t.Fatalf("got %v", err)
	}
}

func TestParseRejectsTrailing(t *testing.T) {
	_, err := Parse(strings.NewReader(`{"version":1,"asset_id":"x","movie_timescale":48000,"tracks":[]} {}`))
	if !errors.Is(err, fault.OfClass(fault.ClassManifest, "trailing-data")) {
		t.Fatalf("got %v", err)
	}
}

func TestValidateDuplicateTrack(t *testing.T) {
	a := valid()
	a.Tracks = append(a.Tracks, a.Tracks[0])
	err := a.Validate()
	if !errors.Is(err, fault.OfClass(fault.ClassManifest, "duplicate-track")) {
		t.Fatalf("got %v", err)
	}
}

func TestValidateChecksLayoutChannelCount(t *testing.T) {
	a := valid()
	a.Tracks[0].Channels = 6
	err := a.Validate()
	if !errors.Is(err, fault.OfClass(fault.ClassAudio, "layout-channels")) {
		t.Fatalf("got %v", err)
	}
}

func TestValidateRejectsUnknownLayout(t *testing.T) {
	a := valid()
	a.Tracks[0].Layout = "quad"
	err := a.Validate()
	if !errors.Is(err, fault.OfClass(fault.ClassAudio, "layout-unknown")) {
		t.Fatalf("got %v", err)
	}
}

func TestValidateRejectsUnsupportedCodec(t *testing.T) {
	a := valid()
	a.Tracks[0].Codec = "aac"
	err := a.Validate()
	if !errors.Is(err, fault.OfClass(fault.ClassManifest, "codec-unsupported")) {
		t.Fatalf("got %v", err)
	}
	var f fault.Fault
	errors.As(err, &f)
	if f.Recovery != fault.Unrecoverable {
		t.Fatalf("required unsupported codec must be fatal, got %v", f.Recovery)
	}
}

func TestValidateOptionalUnsupportedCodecIsRecoverable(t *testing.T) {
	a := valid()
	a.Tracks = []TrackDecl{
		a.Tracks[0],
		{ID: 2, Role: RoleVideo, Codec: "avc1", Required: false, Path: "v.mp4"},
	}
	// optional video is allowed through; graph decides to skip
	if err := a.Validate(); err != nil {
		t.Fatalf("unexpected %v", err)
	}
}

func TestValidateMediaTimescaleMustMatch(t *testing.T) {
	a := valid()
	a.Tracks[0].MediaTimescale = 90000
	err := a.Validate()
	if !errors.Is(err, fault.OfClass(fault.ClassTimeline, "media-timescale")) {
		t.Fatalf("got %v", err)
	}
}

func TestValidateEditDuration(t *testing.T) {
	a := valid()
	a.Tracks[0].Edits = []EditDecl{{MediaTime: 0, Duration: 0}}
	err := a.Validate()
	if !errors.Is(err, fault.OfClass(fault.ClassTimeline, "edit-duration")) {
		t.Fatalf("got %v", err)
	}
}

func TestValidateDialectRate(t *testing.T) {
	a := valid()
	a.Tracks[0].SampleRate = 32000
	a.Tracks[0].MediaTimescale = 32000
	err := a.Validate()
	if !errors.Is(err, fault.OfClass(fault.ClassAudio, "sample-rate")) {
		t.Fatalf("got %v", err)
	}
}

func TestValidateNoTracks(t *testing.T) {
	a := valid()
	a.Tracks = nil
	err := a.Validate()
	if !errors.Is(err, fault.OfClass(fault.ClassDependency, "no-tracks")) {
		t.Fatalf("got %v", err)
	}
}

func TestValidateDuplicateEdge(t *testing.T) {
	a := valid()
	a.Tracks[0].DependsOn = []uint32{2, 2}
	err := a.Validate()
	if !errors.Is(err, fault.OfClass(fault.ClassDependency, "duplicate-edge")) {
		t.Fatalf("got %v", err)
	}
}

func TestValidateSelfEdge(t *testing.T) {
	a := valid()
	a.Tracks[0].DependsOn = []uint32{1}
	err := a.Validate()
	if !errors.Is(err, fault.OfClass(fault.ClassDependency, "self-edge")) {
		t.Fatalf("got %v", err)
	}
}

func TestMovieRate(t *testing.T) {
	a := valid()
	r, err := a.MovieRate()
	if err != nil || r.Num != 48000 || r.Den != 1 {
		t.Fatalf("got %v %v", r, err)
	}
	a.MovieTimescale = 0
	if _, err := a.MovieRate(); err == nil {
		t.Fatal("expected error for zero timescale")
	}
}

func TestCanonicalStable(t *testing.T) {
	a := valid()
	b1, err := a.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := a.Canonical()
	if string(b1) != string(b2) {
		t.Fatal("canonical output is not deterministic")
	}
	if !strings.Contains(string(b1), `"asset_id": "asset-a"`) {
		t.Fatalf("unexpected canonical form:\n%s", b1)
	}
}
