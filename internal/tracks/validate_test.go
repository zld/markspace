package tracks

import (
	"testing"

	"example.com/assetcheck/internal/timeline"
)

func audio(number uint64, ch uint64, rate float64, off int64) Track {
	return Track{
		Number: number, Type: TypeAudio, Offset: off, Language: "jpn",
		Audio: &Audio{Channels: ch, SamplingFrequency: rate},
	}
}

func video(number uint64, off int64, c Colour) Track {
	return Track{
		Number: number, Type: TypeVideo, Offset: off, Language: "und",
		Video: &Video{PixelWidth: 1920, PixelHeight: 1080, Colour: c},
	}
}

func subtitle(number uint64, lang, name string, off int64) Track {
	return Track{Number: number, Type: TypeSubtitle, Offset: off, Language: lang, Name: name}
}

func categories(issues []Issue) []timeline.Category {
	out := make([]timeline.Category, len(issues))
	for i, is := range issues {
		out[i] = is.Category
	}
	return out
}

func findIssue(issues []Issue, field string) *Issue {
	for i := range issues {
		if issues[i].Field == field {
			return &issues[i]
		}
	}
	return nil
}

func TestLayoutChannelMismatch(t *testing.T) {
	ts := []Track{
		{Number: 1, Type: TypeAudio, Offset: 100, Audio: &Audio{
			Channels: 6, SamplingFrequency: 48000, Layout: LayoutStereo,
		}},
	}
	issues := Validate(ts, Profile{})
	if len(issues) != 1 {
		t.Fatalf("issues = %v", issues)
	}
	is := issues[0]
	if is.Category != timeline.CatTrackConflict {
		t.Fatalf("category = %s", is.Category)
	}
	if is.Offset != 100 {
		t.Fatalf("offset = %d, want 100", is.Offset)
	}
	if len(is.Tracks) != 1 || is.Tracks[0] != 1 {
		t.Fatalf("tracks = %v, want [1]", is.Tracks)
	}
	if is.Field != "Audio.ChannelLayout" {
		t.Fatalf("field = %s", is.Field)
	}
}

func TestLayoutFromProfileOverridesTrack(t *testing.T) {
	ts := []Track{{Number: 2, Type: TypeAudio, Offset: 40, Audio: &Audio{
		Channels: 2, SamplingFrequency: 48000,
	}}}
	issues := Validate(ts, Profile{Layouts: map[uint64]ChannelLayout{2: Layout5_1}})
	if len(issues) != 1 {
		t.Fatalf("issues = %v", issues)
	}
	if issues[0].Tracks[0] != 2 {
		t.Fatalf("tracks = %v", issues[0].Tracks)
	}
}

func TestRateOutsideManifestWindow(t *testing.T) {
	ts := []Track{audio(1, 2, 44100, 64)}
	issues := Validate(ts, Profile{AudioRates: map[uint64]RateRange{
		1: {Min: 48000, Max: 96000},
	}})
	if len(issues) != 1 {
		t.Fatalf("issues = %v", issues)
	}
	if issues[0].Field != "Audio.SamplingFrequency" || issues[0].Offset != 64 {
		t.Fatalf("issue = %+v", issues[0])
	}
	// Output rate takes precedence over the input rate.
	ts[0].Audio.OutputSamplingFreq = 48000
	issues = Validate(ts, Profile{AudioRates: map[uint64]RateRange{1: {Min: 48000, Max: 96000}}})
	if len(issues) != 0 {
		t.Fatalf("expected output rate to satisfy window, issues = %v", issues)
	}
}

func TestRateResampleRatioExceedsTwo(t *testing.T) {
	ts := []Track{audio(1, 2, 8000, 10)}
	ts[0].Audio.OutputSamplingFreq = 96000
	issues := Validate(ts, Profile{})
	if findIssue(issues, "Audio.OutputSamplingFrequency") == nil {
		t.Fatalf("expected resample conflict, issues = %v", issues)
	}
	// A 44.1k -> 48k resample is legal.
	ts[0].Audio = &Audio{Channels: 2, SamplingFrequency: 44100, OutputSamplingFreq: 48000}
	if issues := Validate(ts, Profile{}); len(issues) != 0 {
		t.Fatalf("legal resample flagged: %v", issues)
	}
}

func TestMissingChannelCount(t *testing.T) {
	ts := []Track{{Number: 3, Type: TypeAudio, Offset: 200, Audio: &Audio{SamplingFrequency: 48000}}}
	issues := Validate(ts, Profile{})
	if len(issues) != 1 || issues[0].Category != timeline.CatMissingElement {
		t.Fatalf("issues = %v", issues)
	}
}

func TestColourIncompleteNamesMissingFields(t *testing.T) {
	ts := []Track{video(1, 300, Colour{Present: true, MatrixCoefficients: 9, TransferCharacterist: 16})}
	issues := Validate(ts, Profile{RequireColour: true})
	if len(issues) != 1 {
		t.Fatalf("issues = %v", issues)
	}
	if issues[0].Category != timeline.CatMissingElement {
		t.Fatalf("category = %s", issues[0].Category)
	}
	if got := issues[0].Reason; got == "" || !contains(got, "Primaries") {
		t.Fatalf("reason = %q", got)
	}
	if issues[0].Offset != 300 {
		t.Fatalf("offset = %d", issues[0].Offset)
	}
}

func TestColourUnspecifiedIsIncomplete(t *testing.T) {
	// CICP value 2 means "unspecified" and must not pass as complete metadata.
	ts := []Track{video(1, 0, Colour{Present: true, MatrixCoefficients: 2,
		TransferCharacterist: 16, Primaries: 9})}
	issues := Validate(ts, Profile{RequireColour: true})
	if len(issues) != 1 || issues[0].Category != timeline.CatMissingElement {
		t.Fatalf("issues = %v", issues)
	}
}

func TestColourDisagreementAcrossVideoTracks(t *testing.T) {
	good := Colour{Present: true, MatrixCoefficients: 9, TransferCharacterist: 16, Primaries: 9}
	other := Colour{Present: true, MatrixCoefficients: 1, TransferCharacterist: 16, Primaries: 9}
	ts := []Track{video(1, 10, good), video(2, 500, other)}
	issues := Validate(ts, Profile{})
	if len(issues) != 1 {
		t.Fatalf("issues = %v", issues)
	}
	is := issues[0]
	if is.Category != timeline.CatTrackConflict {
		t.Fatalf("category = %s", is.Category)
	}
	if is.Offset != 10 {
		t.Fatalf("offset = %d, want earliest 10", is.Offset)
	}
	if len(is.Tracks) != 2 || is.Tracks[0] != 1 || is.Tracks[1] != 2 {
		t.Fatalf("tracks = %v", is.Tracks)
	}
}

func TestColourDisagreesWithManifestRule(t *testing.T) {
	c := Colour{Present: true, MatrixCoefficients: 9, TransferCharacterist: 16, Primaries: 9}
	ts := []Track{video(1, 7, c)}
	issues := Validate(ts, Profile{VideoColour: map[uint64]ColourRule{
		1: {MatrixCoefficients: 1, Transfer: 1, Primaries: 1},
	}})
	if len(issues) != 1 || issues[0].Field != "Video.Colour" {
		t.Fatalf("issues = %v", issues)
	}
}

func TestDuplicateTrackNumber(t *testing.T) {
	ts := []Track{audio(1, 2, 48000, 100), audio(1, 2, 48000, 400)}
	issues := Validate(ts, Profile{})
	found := false
	for _, is := range issues {
		if is.Category == timeline.CatDuplicate && is.Offset == 400 {
			found = true
		}
	}
	if !found {
		t.Fatalf("duplicate not reported at second offset: %v", issues)
	}
}

func TestSubtitleDistinguishability(t *testing.T) {
	ts := []Track{
		subtitle(3, "jpn", "", 100),
		subtitle(4, "jpn", "", 200),
	}
	issues := Validate(ts, Profile{})
	if len(issues) != 1 || issues[0].Category != timeline.CatTrackConflict {
		t.Fatalf("issues = %v", issues)
	}
	if want := []uint64{3, 4}; issues[0].Tracks[0] != want[0] || issues[0].Tracks[1] != want[1] {
		t.Fatalf("tracks = %v", issues[0].Tracks)
	}
	// Distinct names resolve the ambiguity.
	ts[0].Name, ts[1].Name = "dialogue", "signs"
	if issues := Validate(ts, Profile{}); len(issues) != 0 {
		t.Fatalf("distinct names flagged: %v", issues)
	}
	// Duplicate names are a duplicate, not a general conflict.
	ts[1].Name = "dialogue"
	issues = Validate(ts, Profile{})
	if len(issues) != 1 || issues[0].Category != timeline.CatDuplicate {
		t.Fatalf("issues = %v", issues)
	}
}

func TestIssuesSortedByOffset(t *testing.T) {
	ts := []Track{
		{Number: 5, Type: TypeAudio, Offset: 900, Audio: &Audio{SamplingFrequency: 48000}},
		{Number: 1, Type: TypeAudio, Offset: 100, Audio: &Audio{Channels: 2}},
	}
	issues := Validate(ts, Profile{})
	if len(issues) != 2 {
		t.Fatalf("issues = %v", issues)
	}
	if issues[0].Offset != 100 || issues[1].Offset != 900 {
		t.Fatalf("not sorted: %v", issues)
	}
}

func TestValidProfilePasses(t *testing.T) {
	c := Colour{Present: true, MatrixCoefficients: 9, TransferCharacterist: 16,
		Primaries: 9, Range: 1, BitsPerChannel: 10}
	ts := []Track{
		video(1, 0, c),
		audio(2, 2, 48000, 100),
		audio(3, 6, 48000, 200),
		subtitle(4, "eng", "dialogue", 300),
		subtitle(5, "jpn", "dialogue", 400),
	}
	ts[1].Audio.Layout = LayoutStereo
	ts[2].Audio.Layout = Layout5_1
	profile := Profile{
		AudioRates:    map[uint64]RateRange{2: {48000, 48000}, 3: {48000, 48000}},
		Layouts:       map[uint64]ChannelLayout{2: LayoutStereo, 3: Layout5_1},
		MaxChannels:   8,
		RequireColour: true,
	}
	if issues := Validate(ts, profile); len(issues) != 0 {
		t.Fatalf("valid media rejected: %v", issues)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
