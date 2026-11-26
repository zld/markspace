package manifest_test

import (
	"strings"
	"testing"

	"assetcheck/internal/manifest"
	"assetcheck/internal/media"
)

func ms(v int64) media.Rational {
	return media.MustRat(v, 1000)
}

func validRequest() manifest.Request {
	return manifest.Request{
		AssetID:           "asset-1",
		ContentSHA256:     "content-sha",
		AnalyzerVersion:   "analyzer-v1",
		RequiredLanguages: []string{"en", "zh"},
		Duration:          media.Second,
		Metrics: manifest.Metrics{
			TargetBPS: 768000, ActualBPS: 768000,
			DeltaPct: 0, PSNRDB: 120, AlignMaxMS: 0,
		},
		Tracks: []manifest.TrackInput{
			{TrackID: 1, Language: "en", TrackType: "audio"},
			{TrackID: 2, Language: "zh", TrackType: "audio"},
			{TrackID: 3, Language: "en", TrackType: "subtitle"},
			{TrackID: 4, Language: "zh", TrackType: "subtitle"},
		},
		Cues: []manifest.Cue{
			{CueID: "cue-1", Language: "en", TrackID: 3, Start: ms(0), End: ms(500)},
			{CueID: "cue-1", Language: "en", TrackID: 3, Start: ms(0), End: ms(500)},
			{CueID: "cue-1", Language: "zh", TrackID: 4, Start: ms(0), End: ms(500)},
		},
		ExpectedCues: []manifest.ExpectedCue{
			{CueID: "cue-1", Start: ms(0), End: ms(500)},
		},
	}
}

func TestMergeDeduplicatesCuesAndSortsStableItems(t *testing.T) {
	m, err := manifest.Merge(validRequest())
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Cues) != 2 {
		t.Fatalf("cues = %d, want one per language after duplicate removal", len(m.Cues))
	}
	if len(m.Items) != 4 {
		t.Fatalf("items = %d, want 4 tracks", len(m.Items))
	}
	for i := 1; i < len(m.Items); i++ {
		if m.Items[i-1].StableKey >= m.Items[i].StableKey {
			t.Fatalf("items not sorted at %d: %q >= %q", i, m.Items[i-1].StableKey, m.Items[i].StableKey)
		}
	}
	for _, item := range m.Items {
		if item.StableKey != manifest.StableKey("asset-1", item.Language, item.TrackType, "content-sha") {
			t.Fatalf("item key does not match required identity: %+v", item)
		}
	}

	b1, sha1, err := manifest.Encode(m)
	if err != nil {
		t.Fatal(err)
	}
	b2, sha2, err := manifest.Encode(m)
	if err != nil {
		t.Fatal(err)
	}
	if string(b1) != string(b2) || sha1 != sha2 {
		t.Fatal("manifest encoding is not deterministic")
	}
}

func TestStableKeyHasNoDeliveryOrdinal(t *testing.T) {
	want := manifest.StableKey("asset", "zh", "subtitle", "digest")
	if got := manifest.StableKey("asset", "zh", "subtitle", "digest"); got != want {
		t.Fatal("same identity produced different stable key")
	}
	if got := manifest.StableKey("asset", "en", "subtitle", "digest"); got == want {
		t.Fatal("language did not affect stable key")
	}
	if got := manifest.StableKey("asset", "zh", "audio", "digest"); got == want {
		t.Fatal("track type did not affect stable key")
	}
	if got := manifest.StableKey("asset", "zh", "subtitle", "other"); got == want {
		t.Fatal("content digest did not affect stable key")
	}
}

func TestMergeRejectsMissingRequiredLanguageTrack(t *testing.T) {
	req := validRequest()
	req.Tracks = req.Tracks[:3] // remove zh subtitle
	_, err := manifest.Merge(req)
	me, ok := media.AsError(err)
	if !ok || me.Code != "MANIFEST_MISSING_LANGUAGE_TRACK" {
		t.Fatalf("error = %v, want missing language track", err)
	}
	if media.ExitCode(err) != 3 {
		t.Fatalf("exit code = %d, want 3", media.ExitCode(err))
	}
	if !strings.Contains(me.Detail, "zh") {
		t.Fatalf("detail %q does not identify language", me.Detail)
	}
}

func TestMergeRejectsMissingCue(t *testing.T) {
	req := validRequest()
	req.ExpectedCues = append(req.ExpectedCues, manifest.ExpectedCue{
		CueID: "cue-2", Start: ms(700), End: ms(900),
	})
	_, err := manifest.Merge(req)
	me, ok := media.AsError(err)
	if !ok || me.Code != "MANIFEST_CUE_MISSING" {
		t.Fatalf("error = %v, want missing cue", err)
	}
	if !strings.Contains(me.Detail, "cue-2") {
		t.Fatalf("detail %q lacks cue id", me.Detail)
	}
}

func TestMergeRejectsOutOfBoundsCue(t *testing.T) {
	req := validRequest()
	req.Cues[0].End = ms(1200)
	req.Cues[1].End = ms(1200)
	_, err := manifest.Merge(req)
	me, ok := media.AsError(err)
	if !ok || me.Code != "MANIFEST_CUE_OUT_OF_BOUNDS" {
		t.Fatalf("error = %v, want out-of-bounds cue", err)
	}
	if me.TrackID == 0 || !strings.Contains(me.Detail, "cue-1") {
		t.Fatalf("error location track=%d detail=%q is not locatable", me.TrackID, me.Detail)
	}
	if media.ExitCode(err) != 3 {
		t.Fatalf("exit code = %d, want 3", media.ExitCode(err))
	}
}

func TestMergeRejectsNonPositiveCueIntervalAndBoundaryMismatch(t *testing.T) {
	req := validRequest()
	req.Cues[0].End = ms(0)
	_, err := manifest.Merge(req)
	if me, ok := media.AsError(err); !ok || me.Code != "MANIFEST_CUE_BAD_INTERVAL" {
		t.Fatalf("interval error = %v", err)
	}

	req = validRequest()
	req.ExpectedCues[0].End = ms(400)
	_, err = manifest.Merge(req)
	if me, ok := media.AsError(err); !ok || me.Code != "MANIFEST_CUE_BOUNDARY_MISMATCH" {
		t.Fatalf("boundary error = %v", err)
	}
}
