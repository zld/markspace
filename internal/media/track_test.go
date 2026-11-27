package media

import (
	"testing"

	"continuity/internal/cerr"
	"continuity/internal/fixture"
)

func programTracks() []TrackParams {
	return []TrackParams{
		{Number: 1, Kind: "video", Source: "matroska", ColorSpace: "bt709"},
		{Number: 2, Kind: "audio", Source: "matroska", Channels: 2, SampleRate: 48000},
		{Number: 1, Kind: "video", Source: "mp4-init", ColorSpace: "bt601"},
		{Number: 3, Kind: "audio", Source: "rtp", Channels: 2, SampleRate: 44100},
	}
}

func TestNormalizeDecisions(t *testing.T) {
	norm, decisions, err := Normalize(programTracks())
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(norm) != 3 {
		t.Fatalf("tracks = %d, want 3", len(norm))
	}
	if len(decisions) != 2 {
		t.Fatalf("decisions = %d, want 2: %+v", len(decisions), decisions)
	}
	var color, rate *Decision
	for i := range decisions {
		switch decisions[i].Aspect {
		case "color":
			color = &decisions[i]
		case "sample-rate":
			rate = &decisions[i]
		}
	}
	if color == nil || color.From != "bt601" || color.To != "bt709" || color.Track != 1 {
		t.Fatalf("color decision = %+v", color)
	}
	if rate == nil || rate.From != "44100" || rate.To != "48000" || rate.Track != 3 {
		t.Fatalf("rate decision = %+v", rate)
	}
	// The RTP track is normalized to the program rate.
	if norm[2].SampleRate != 48000 {
		t.Fatalf("normalized rate = %d", norm[2].SampleRate)
	}
}

func TestUnsupportedChannelLayout(t *testing.T) {
	tracks := []TrackParams{
		{Number: 2, Kind: "audio", Source: "matroska", Channels: 3, SampleRate: 48000},
	}
	_, _, err := Normalize(tracks)
	if !cerr.Is(err, cerr.CodeLayoutUnsupported) {
		t.Fatalf("err = %v, want %s", err, cerr.CodeLayoutUnsupported)
	}
	ce := err.(*cerr.Error)
	if ce.Path != "track[2]" {
		t.Fatalf("path = %q, want track[2]", ce.Path)
	}
}

func TestUnsupportedSampleRate(t *testing.T) {
	tracks := []TrackParams{
		{Number: 2, Kind: "audio", Source: "matroska", Channels: 2, SampleRate: 4000},
	}
	if _, _, err := Normalize(tracks); !cerr.Is(err, cerr.CodeRateUnsupported) {
		t.Fatalf("err = %v, want %s", err, cerr.CodeRateUnsupported)
	}
}

func TestConflictingColourMetadata(t *testing.T) {
	tracks := []TrackParams{
		{Number: 1, Kind: "video", Source: "matroska", ColorSpace: "reserved(3)"},
	}
	_, _, err := Normalize(tracks)
	if !cerr.Is(err, cerr.CodeMetadataConflict) {
		t.Fatalf("err = %v, want %s", err, cerr.CodeMetadataConflict)
	}
	ce := err.(*cerr.Error)
	if ce.Path != "track[1]" {
		t.Fatalf("path = %q, want track[1]", ce.Path)
	}
}

func TestParseMP4Colour(t *testing.T) {
	in := fixture.Generate(17, "")
	c, err := ParseMP4Colour(in.MP4Init)
	if err != nil {
		t.Fatalf("ParseMP4Colour: %v", err)
	}
	if c.Matrix != 5 || c.Primaries != 1 || c.Transfer != 1 {
		t.Fatalf("colour = %+v", c)
	}
	name, ok := ColorName(uint64(c.Matrix))
	if !ok || name != "bt601" {
		t.Fatalf("name = %q ok = %v", name, ok)
	}
}

func TestParseMP4ColourMissing(t *testing.T) {
	// ftyp only, no colr box.
	init := []byte{0, 0, 0, 16, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm', 0, 0, 0, 0}
	if _, err := ParseMP4Colour(init); !cerr.Is(err, cerr.CodeMetadataConflict) {
		t.Fatalf("err = %v, want %s", err, cerr.CodeMetadataConflict)
	}
}
