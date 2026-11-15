package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hlsforge/internal/fixture"
	"hlsforge/internal/ingest"
	"hlsforge/internal/segment"
	"hlsforge/internal/timeline"
)

func buildLayouts(t *testing.T, inbox string, seqs int) []TrackInput {
	t.Helper()
	res, err := ingest.Collect(inbox, 1, seqs, 500)
	if err != nil {
		t.Fatal(err)
	}
	var inputs []TrackInput
	for _, in := range res.Tracks {
		tr, err := timeline.Build(in)
		if err != nil {
			t.Fatal(err)
		}
		layout, err := segment.Layout(tr, 2000, 500, 0)
		if err != nil {
			t.Fatal(err)
		}
		inputs = append(inputs, TrackInput{
			Bitrate: in.Bitrate, Layout: layout,
			InitURI:   filepath.Join(layout.SegmentDir, fixture.InitName()),
			CanSkipTo: true, HasPreload: true,
		})
	}
	return inputs
}

func findInput(t *testing.T, inputs []TrackInput, bitrate int) TrackInput {
	t.Helper()
	for _, in := range inputs {
		if in.Bitrate == bitrate {
			return in
		}
	}
	t.Fatalf("input for bitrate %d not found", bitrate)
	return TrackInput{}
}

func TestMasterAndMediaPlaylistContent(t *testing.T) {
	inbox := t.TempDir()
	if _, err := fixture.Generate(inbox, []int{800000, 1600000}, fixture.Options{
		Seed: 7, PartMs: 500, DurationMs: 4000,
	}); err != nil {
		t.Fatal(err)
	}
	inputs := buildLayouts(t, inbox, 8)
	master := MasterPlaylist(inputs)
	for _, want := range []string{
		"#EXTM3U", "#EXT-X-VERSION:9",
		"#EXT-X-STREAM-INF:BANDWIDTH=800000",
		"#EXT-X-STREAM-INF:BANDWIDTH=1600000",
		"800000.m3u8", "1600000.m3u8",
	} {
		if !strings.Contains(master, want) {
			t.Fatalf("master missing %q:\n%s", want, master)
		}
	}
	media := MediaPlaylist(findInput(t, inputs, 800000))
	for _, want := range []string{
		"#EXT-X-TARGETDURATION:2",
		"#EXT-X-MEDIA-SEQUENCE:0",
		"#EXT-X-MAP:URI=\"800000/init.mp4\"",
		"#EXT-X-PART-INF:PART-TARGET=0.500",
		"#EXT-X-PART:", "DURATION=0.500", "INDEPENDENT=YES",
		"#EXTINF:2.000,", "800000/seg-00000.mp4",
		"#EXT-X-PRELOAD-HINT:TYPE=PART",
		"#EXT-X-ENDLIST",
	} {
		if !strings.Contains(media, want) {
			t.Fatalf("media missing %q:\n%s", want, media)
		}
	}
}

func TestMediaPlaylistKeepsGapMarkers(t *testing.T) {
	inbox := t.TempDir()
	metas, err := fixture.Generate(inbox, []int{1600000}, fixture.Options{
		Seed: 7, PartMs: 500, DurationMs: 4000, Faults: []string{"missing"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := metas[0].Missing; len(got) != 1 || got[0] != 5 {
		t.Fatalf("missing seq = %v, want [5]", got)
	}
	inputs := buildLayouts(t, inbox, 8)
	media := MediaPlaylist(findInput(t, inputs, 1600000))
	if !strings.Contains(media, "#EXT-X-GAP:SEQ=5") {
		t.Fatalf("playlist must retain explicit GAP slot:\n%s", media)
	}
	// Segment 1 still has three content parts, so it remains a normal EXTINF
	// segment rather than an all-gap segment.
	if !strings.Contains(media, "1600000/seg-00001.mp4") {
		t.Fatalf("partial-gap segment must still list its media file:\n%s", media)
	}
}

func TestMediaPlaylistAllGapSegment(t *testing.T) {
	inbox := t.TempDir()
	metas, err := fixture.Generate(inbox, []int{1600000}, fixture.Options{
		Seed: 7, PartMs: 500, DurationMs: 4000,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Remove the entire first segment (seqs 1..4) to force an all-gap segment.
	for seq := 1; seq <= 4; seq++ {
		if err := os.Remove(filepath.Join(metas[0].Dir, fixture.FragmentName(seq))); err != nil {
			t.Fatal(err)
		}
	}
	var order []string
	for seq := 5; seq <= 8; seq++ {
		order = append(order, fixture.FragmentName(seq))
	}
	if err := os.WriteFile(filepath.Join(metas[0].Dir, fixture.IndexName()),
		[]byte(strings.Join(order, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	inputs := buildLayouts(t, inbox, 8)
	media := MediaPlaylist(findInput(t, inputs, 1600000))
	if !strings.Contains(media, "#EXT-X-GAP:SEGMENT=0") {
		t.Fatalf("all-gap segment marker missing:\n%s", media)
	}
	if !strings.Contains(media, "1600000/seg-00001.mp4") {
		t.Fatalf("later content segment must remain:\n%s", media)
	}
}

func TestWriteAllAtomicAndRebuildStable(t *testing.T) {
	inbox := t.TempDir()
	out := t.TempDir()
	if _, err := fixture.Generate(inbox, []int{800000}, fixture.Options{
		Seed: 7, PartMs: 500, DurationMs: 4000,
	}); err != nil {
		t.Fatal(err)
	}
	inputs := buildLayouts(t, inbox, 8)
	if err := WriteAll(out, "master.m3u8", inputs); err != nil {
		t.Fatal(err)
	}
	// Re-running with identical inputs must produce byte-identical output.
	first, err := os.ReadFile(filepath.Join(out, "800000.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteAll(out, "master.m3u8", inputs); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(filepath.Join(out, "800000.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("canonical manifest rebuild is not stable")
	}
}
