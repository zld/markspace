package ingest

import (
	"os"
	"path/filepath"
	"testing"

	"mediavault/internal/fixture"
	"mediavault/internal/timeline"
)

func demoSpec() fixture.MP4Spec {
	return fixture.MP4Spec{Tracks: []fixture.MediaSpec{
		{Kind: 'v', Timescale: 30000, FrameCount: 6, SampleSize: 32, FrameDelta: 1001, Key: 1},
		{Kind: 'a', Timescale: 48000, FrameCount: 4, SampleSize: 16, FrameDelta: 1024, Key: 9},
	}}
}

func TestParseMP4RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.mp4")
	if err := fixture.WriteMP4(path, demoSpec()); err != nil {
		t.Fatal(err)
	}
	res, err := ParseMP4(path, "demo")
	if err != nil {
		t.Fatalf("ParseMP4: %v", err)
	}
	if len(res.Tracks) != 2 {
		t.Fatalf("tracks = %d, want 2", len(res.Tracks))
	}
	vid := res.Tracks[0]
	if vid.ID.Kind != timeline.KindVideo || vid.Timescale != 30000 {
		t.Fatalf("video track meta wrong: %+v", vid.ID)
	}
	if len(vid.Samples) != 6 {
		t.Fatalf("video samples = %d, want 6", len(vid.Samples))
	}
	for i, s := range vid.Samples {
		if len(s.Payload) != 32 {
			t.Fatalf("sample %d payload = %d bytes", i, len(s.Payload))
		}
		if i > 0 && s.TS.DTS.Cmp(vid.Samples[i-1].TS.DTS) <= 0 {
			t.Fatalf("sample %d DTS not strictly increasing", i)
		}
	}
	// VFR: 1001/30000 ticks must survive as exact rationals (~33.366ms).
	got := vid.Samples[1].TS.DTS.Sub(vid.Samples[0].TS.DTS).Nanoseconds()
	if got != 33_366_666 {
		t.Fatalf("frame delta = %d ns, want 33366666 (no average-rate inference)", got)
	}
	aud := res.Tracks[1]
	if aud.ID.Kind != timeline.KindAudio || len(aud.Samples) != 4 {
		t.Fatalf("audio track wrong: %+v", aud)
	}
}

func TestParseMP4CrossBoxTruncation(t *testing.T) {
	dir := t.TempDir()
	// Cut inside the mdat payload while keeping every box header intact:
	// the structural scan succeeds, but a sample's byte range overruns EOF.
	full, err := fixture.BuildMP4(demoSpec())
	if err != nil {
		t.Fatal(err)
	}
	cut := len(full) - 30
	path := filepath.Join(dir, "trunc.mp4")
	if err := fixture.TruncateFile(path, full, cut); err != nil {
		t.Fatal(err)
	}
	res, err := ParseMP4(path, "demo")
	if err != nil {
		t.Fatalf("structural parse should still succeed, got %v", err)
	}
	// At least one track must carry a truncation error with a real offset.
	found := false
	for _, tr := range res.Tracks {
		for _, se := range tr.Errors {
			if se.Kind == timeline.KindTruncatedBox && se.Offset > 0 {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("expected truncated-box StageError with offset, got %+v", res.Tracks)
	}
}

func TestParseMP4IllegalBoxLength(t *testing.T) {
	dir := t.TempDir()
	full, err := fixture.BuildMP4(demoSpec())
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt the ftyp size field (first u32) to an impossible value.
	full[0] = 0x00
	full[1] = 0x00
	full[2] = 0x00
	full[3] = 0x03
	path := filepath.Join(dir, "badlen.mp4")
	if err := fixture.TruncateFile(path, full, len(full)); err != nil {
		t.Fatal(err)
	}
	_, err = ParseMP4(path, "demo")
	if err == nil {
		t.Fatal("expected illegal-box-length error")
	}
	se, ok := err.(*timeline.StageError)
	if !ok || se.Kind != timeline.KindIllegalBoxLength || se.Offset != 0 {
		t.Fatalf("want illegal-box-length at offset 0, got %#v", err)
	}
}

func TestParseMP4MissingTracks(t *testing.T) {
	dir := t.TempDir()
	// A file containing only ftyp has no trak/mdat at all.
	path := filepath.Join(dir, "lonely.mp4")
	if err := os.WriteFile(path, fixture.StandaloneFtyp(), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ParseMP4(path, "demo")
	se, ok := err.(*timeline.StageError)
	if !ok || se.Kind != timeline.KindMissingTrack {
		t.Fatalf("want missing-track StageError, got %#v", err)
	}
}

func TestParseMP4TrackIsolation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.mp4")
	if err := fixture.WriteMP4(path, demoSpec()); err != nil {
		t.Fatal(err)
	}
	res, err := ParseMP4(path, "demo")
	if err != nil {
		t.Fatal(err)
	}
	// A failing track leaves the sibling's samples intact.
	if len(res.Tracks[0].Samples) == 0 || len(res.Tracks[1].Samples) == 0 {
		t.Fatal("both tracks must parse independently")
	}
}
