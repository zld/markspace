package pipeline

import (
	"path/filepath"
	"testing"

	"mediatool/internal/fixture"
	"mediatool/internal/mapping"
	"mediatool/internal/mp4"
)

// TestStartOffsetNotPaddedAsSamples proves that a non-zero track start
// time and a gap between fragments are preserved as timeline spacing,
// never converted into synthetic media samples.
func TestStartOffsetNotPaddedAsSamples(t *testing.T) {
	g := fixture.Generate(21, 2, -1)
	rules := []mapping.Rule{
		{OutputSlot: "video-main", TrackID: 1, MediaType: "video"},
	}
	pipe, err := New(rules, filepath.Join(t.TempDir(), "out"))
	if err != nil {
		t.Fatal(err)
	}
	pipe.SetTracks(g.Tracks)

	f0 := g.VideoFrags[0]
	f1 := g.VideoFrags[1]
	// Fragment 0 starts at a large non-zero offset; fragment 1 leaves a
	// deliberate gap instead of following contiguously.
	startOffset := uint64(90000) // 1 second at 90 kHz
	gapBase := startOffset + uint64(len(f0.Durations))*4000 + 50000
	spec0 := mp4.FragmentSpec{
		Seq: 0, TrackID: 1, BaseMediaDecode: startOffset,
		SampleDurations: f0.Durations, SampleSizes: f0.Sizes, SamplePayload: f0.Payload,
	}
	spec1 := mp4.FragmentSpec{
		Seq: 1, TrackID: 1, BaseMediaDecode: gapBase,
		SampleDurations: f1.Durations, SampleSizes: f1.Sizes, SamplePayload: f1.Payload,
	}
	pipe.Ingest(Input{Data: mp4.BuildFragment(spec0)})
	pipe.Ingest(Input{Data: mp4.BuildFragment(spec1)})
	res, err := pipe.Finalize()
	if err != nil {
		t.Fatal(err)
	}

	slot := res.Manifest.Slots[0]
	if len(slot.Fragments) != 2 {
		t.Fatalf("gap must not create fragments: got %d", len(slot.Fragments))
	}
	// The start offset and gap survive in corrected DTS rather than being
	// flattened to a zero-based contiguous timeline.
	if slot.Fragments[0].CorrectedDTS != startOffset {
		t.Fatalf("start offset lost: %d != %d",
			slot.Fragments[0].CorrectedDTS, startOffset)
	}
	if slot.Fragments[1].CorrectedDTS != gapBase {
		t.Fatalf("gap base lost: %d != %d",
			slot.Fragments[1].CorrectedDTS, gapBase)
	}
	// Sample counts equal the input exactly — no padding samples.
	wantSamples := len(f0.Durations) + len(f1.Durations)
	gotSamples := slot.Fragments[0].SampleCount + slot.Fragments[1].SampleCount
	if gotSamples != wantSamples {
		t.Fatalf("sample count changed by padding: got %d want %d",
			gotSamples, wantSamples)
	}
}

// TestAudioMappedOnOwnTimescale ensures audio uses its 48 kHz time base
// and is never reinterpreted using the video timescale.
func TestAudioMappedOnOwnTimescale(t *testing.T) {
	g := fixture.Generate(31, 3, -1)
	rules := []mapping.Rule{
		{OutputSlot: "audio-main", TrackID: 2, MediaType: "audio"},
	}
	pipe, err := New(rules, filepath.Join(t.TempDir(), "out"))
	if err != nil {
		t.Fatal(err)
	}
	pipe.SetTracks(g.Tracks)
	for _, f := range g.AudioFrags {
		pipe.Ingest(Input{Data: f.Bytes})
	}
	res, err := pipe.Finalize()
	if err != nil {
		t.Fatal(err)
	}
	slot := res.Manifest.Slots[0]
	if slot.Timescale != 48000 {
		t.Fatalf("audio timescale = %d, want 48000", slot.Timescale)
	}
	// First audio fragment starts at zero; second starts one grid window
	// later expressed in 48 kHz units (3072), not 90 kHz units (5760).
	if len(slot.Fragments) >= 2 &&
		slot.Fragments[1].CorrectedDTS != 3072 {
		t.Fatalf("audio DTS not on 48 kHz grid: %d",
			slot.Fragments[1].CorrectedDTS)
	}
}
