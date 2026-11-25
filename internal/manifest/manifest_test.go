package manifest

import (
	"errors"
	"strings"
	"testing"

	"example.com/assetcheck/internal/timeline"
)

const v2Doc = `{
  "version": 2,
  "asset_id": "game-ep01",
  "title": "Episode 1",
  "languages": ["jpn", "eng"],
  "max_channels": 8,
  "require_colour": true,
  "sequence_base": 0,
  "audio": {
    "2": {"sample_rate_hz": {"min_hz": 48000, "max_hz": 48000}, "channel_layout": "stereo"},
    "3": {"sample_rate_hz": {"min_hz": 48000, "max_hz": 96000}, "channel_layout": "5.1"}
  },
  "video": {
    "1": {"colour": {"matrix_coefficients": 9, "transfer_characteristics": 16, "primaries": 9, "range": 1}}
  },
  "tracks": [
    {"number": 1, "type": "video", "codec_id": "V_MPEG4/ISO/AVC", "language": "jpn", "name": "main"},
    {"number": 2, "type": "audio", "codec_id": "A_OPUS", "language": "jpn", "name": "dialogue"},
    {"number": 3, "type": "audio", "codec_id": "A_OPUS", "language": "eng", "name": "dialogue"},
    {"number": 4, "type": "subtitle", "codec_id": "S_TEXT/UTF8", "language": "eng", "name": "signs"}
  ]
}`

const legacyDoc = `{
  "version": 1,
  "asset_id": "game-ep01",
  "title": "Episode 1",
  "languages": ["jpn", "eng"],
  "max_channels": 8,
  "require_colour": true,
  "sequence_base": 0,
  "audio": {
    "2": {"rate_hz": {"min": 48000, "max": 48000}, "layout": "stereo"},
    "3": {"rate_hz": {"min": 48000, "max": 96000}, "layout": "5.1"}
  },
  "video": {
    "1": {"colour": {"matrix": 9, "transfer": 16, "primaries": 9, "range": 1}}
  },
  "tracks": [
    {"number": 1, "type": "video", "codec_id": "V_MPEG4/ISO/AVC", "language": "jpn", "name": "main"},
    {"number": 2, "type": "audio", "codec_id": "A_OPUS", "language": "jpn", "name": "dialogue"},
    {"number": 3, "type": "audio", "codec_id": "A_OPUS", "language": "eng", "name": "dialogue"},
    {"number": 4, "type": "subtitle", "codec_id": "S_TEXT/UTF8", "language": "eng", "name": "signs"}
  ]
}`

func TestV2AndLegacyAdaptToSameCanonicalForm(t *testing.T) {
	v2, err := Parse([]byte(v2Doc))
	if err != nil {
		t.Fatalf("v2: %v", err)
	}
	legacy, err := Parse([]byte(legacyDoc))
	if err != nil {
		t.Fatalf("legacy: %v", err)
	}
	if v2.Version != 2 || legacy.Version != 1 {
		t.Fatalf("versions = %d, %d", v2.Version, legacy.Version)
	}
	if v2.AudioRates[3] != legacy.AudioRates[3] {
		t.Fatalf("rate mismatch: %+v vs %+v", v2.AudioRates[3], legacy.AudioRates[3])
	}
	if v2.Layouts[2] != "stereo" || legacy.Layouts[2] != "stereo" {
		t.Fatalf("layouts: %v %v", v2.Layouts, legacy.Layouts)
	}
	if v2.VideoColour[1] != legacy.VideoColour[1] {
		t.Fatalf("colour mismatch: %+v vs %+v", v2.VideoColour[1], legacy.VideoColour[1])
	}
	if len(v2.Tracks) != 4 || len(legacy.Tracks) != 4 {
		t.Fatalf("tracks: %d %d", len(v2.Tracks), len(legacy.Tracks))
	}
	if v2.SequenceBase != 0 || legacy.SequenceBase != 0 {
		t.Fatalf("sequence base = %d, %d", v2.SequenceBase, legacy.SequenceBase)
	}
	if v2.Digest() == legacy.Digest() {
		t.Fatal("different raw documents must have different digests")
	}
	if err := v2.ValidateCrossFields(); err != nil {
		t.Fatalf("v2 cross-field: %v", err)
	}
	if err := legacy.ValidateCrossFields(); err != nil {
		t.Fatalf("legacy cross-field: %v", err)
	}
	// Field paths must point at the version's own spelling.
	if got := v2.FieldMap["audio.2.sample_rate_hz"]; got != "audio.2.sample_rate_hz" {
		t.Fatalf("v2 field path = %q", got)
	}
	if got := legacy.FieldMap["audio.2.sample_rate_hz"]; got != "audio.2.rate_hz" {
		t.Fatalf("legacy field path = %q", got)
	}
	if got := legacy.FieldMap["video.1.colour"]; got != "video.1.colour" {
		t.Fatalf("legacy colour path = %q", got)
	}
}

func TestUnknownVersionStopsWithVersionCategory(t *testing.T) {
	_, err := Parse([]byte(`{"version": 99, "asset_id": "x"}`))
	if err == nil {
		t.Fatal("expected version error")
	}
	if !IsVersionError(err) {
		t.Fatalf("category = %s, err = %v", timeline.CategoryOf(err), err)
	}
	if !strings.Contains(err.Error(), "99") {
		t.Fatalf("error does not name the version: %v", err)
	}
	if code := timeline.ExitCodeOf(err); code != timeline.ExitBadInput {
		t.Fatalf("exit = %d", code)
	}
}

func TestMissingVersionIsFieldError(t *testing.T) {
	_, err := Parse([]byte(`{"asset_id": "x"}`))
	if !IsFieldError(err) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "version") {
		t.Fatalf("err = %v", err)
	}
}

func TestUnmappableFieldStopsWithPath(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		path string
	}{
		{
			name: "unknown top-level key",
			doc:  `{"version": 2, "asset_id": "x", "audio_rate_override": 1}`,
			path: "audio_rate_override",
		},
		{
			name: "legacy spelling inside v2",
			doc:  `{"version": 2, "audio": {"2": {"rate_hz": {"min": 48000, "max": 48000}}}}`,
			path: "audio.2.rate_hz",
		},
		{
			name: "unknown colour field",
			doc: `{"version": 2, "video": {"1": {"colour": {"matrix_coefficients": 9,
				"transfer_characteristics": 16, "primaries": 9, "hdr10_plus": true}}}}`,
			path: "video.1.colour.hdr10_plus",
		},
		{
			name: "legacy track-level rate",
			doc:  `{"version": 1, "tracks": [{"number": 2, "rate_hz": {"min": 48000, "max": 48000}}]}`,
			path: "tracks[0].rate_hz",
		},
		{
			name: "unknown track field",
			doc:  `{"version": 2, "tracks": [{"number": 2, "bitrate_kbps": 128}]}`,
			path: "tracks[0].bitrate_kbps",
		},
		{
			name: "unknown rate window key",
			doc:  `{"version": 2, "audio": {"2": {"sample_rate_hz": {"min_hz": 48000, "max_hz": 48000, "target_hz": 48000}}}}`,
			path: "audio.2.sample_rate_hz.target_hz",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.doc))
			if err == nil {
				t.Fatal("expected error")
			}
			if !IsFieldError(err) {
				t.Fatalf("category = %s", timeline.CategoryOf(err))
			}
			var te *timeline.Error
			if !asTimeline(err, &te) {
				t.Fatalf("not a timeline error: %T", err)
			}
			if te.Field != tc.path {
				t.Fatalf("field path = %q, want %q (err=%v)", te.Field, tc.path, err)
			}
		})
	}
}

func TestIncompleteColourRuleRejected(t *testing.T) {
	doc := `{"version": 2, "video": {"1": {"colour": {"matrix_coefficients": 9}}}}`
	_, err := Parse([]byte(doc))
	if !IsFieldError(err) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "colour") {
		t.Fatalf("err = %v", err)
	}
}

func TestInvalidRateWindowRejected(t *testing.T) {
	doc := `{"version": 1, "audio": {"2": {"rate_hz": {"min": 96000, "max": 48000}}}}`
	_, err := Parse([]byte(doc))
	if !IsFieldError(err) {
		t.Fatalf("err = %v", err)
	}
}

func TestCrossFieldUndeclaredTrack(t *testing.T) {
	doc := `{"version": 2, "tracks": [{"number": 1}],
		"audio": {"9": {"sample_rate_hz": {"min_hz": 48000, "max_hz": 48000}}}}`
	m, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	err = m.ValidateCrossFields()
	if err == nil {
		t.Fatal("expected cross-field error")
	}
	if !strings.Contains(err.Error(), "track 9") {
		t.Fatalf("err = %v", err)
	}
}

func TestMalformedJSONIsFieldError(t *testing.T) {
	_, err := Parse([]byte(`{"version": 2,`))
	if err == nil {
		t.Fatal("expected error")
	}
	if timeline.ExitCodeOf(err) != timeline.ExitBadInput {
		t.Fatalf("exit = %d", timeline.ExitCodeOf(err))
	}
}

func TestDigestIsStable(t *testing.T) {
	a, err := Parse([]byte(v2Doc))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Parse([]byte(v2Doc))
	if err != nil {
		t.Fatal(err)
	}
	if a.Digest() != b.Digest() {
		t.Fatalf("digests differ: %s vs %s", a.Digest(), b.Digest())
	}
	if len(a.Digest()) != 64 {
		t.Fatalf("digest length = %d", len(a.Digest()))
	}
}

func asTimeline(err error, target **timeline.Error) bool {
	return errors.As(err, target)
}
