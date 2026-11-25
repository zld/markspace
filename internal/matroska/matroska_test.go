package matroska

import (
	"os"
	"path/filepath"
	"testing"

	"example.com/assetcheck/internal/fragments"
	"example.com/assetcheck/internal/media"
	"example.com/assetcheck/internal/timeline"
	"example.com/assetcheck/internal/tracks"
	"example.com/assetcheck/testdata"
)

func fixtures(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := testdata.Generate(dir, 17); err != nil {
		t.Fatalf("generate: %v", err)
	}
	return dir
}

func TestParseValidFile(t *testing.T) {
	dir := fixtures(t)
	a, err := Parse(filepath.Join(dir, testdata.ValidMKV))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if a.Container != media.ContainerMatroska {
		t.Fatalf("container = %s", a.Container)
	}
	if a.Version != 4 {
		t.Fatalf("version = %d", a.Version)
	}
	if a.TimecodeScale != 1_000_000 {
		t.Fatalf("timecode scale = %d", a.TimecodeScale)
	}
	if len(a.Tracks) != 5 {
		t.Fatalf("tracks = %d", len(a.Tracks))
	}
	// Track numbers must be sorted so report output is stable.
	for i := 1; i < len(a.Tracks); i++ {
		if a.Tracks[i].Number <= a.Tracks[i-1].Number {
			t.Fatalf("tracks not sorted: %v", a.Tracks)
		}
	}
	video := a.TrackByNumber(1)
	if video == nil || video.Video == nil {
		t.Fatal("video track metadata missing")
	}
	if video.Video.PixelWidth != 1920 || video.Video.PixelHeight != 1080 {
		t.Fatalf("video dimensions = %dx%d", video.Video.PixelWidth, video.Video.PixelHeight)
	}
	if !video.Video.Colour.IsComplete() {
		t.Fatalf("colour metadata incomplete: %+v", video.Video.Colour)
	}
	if video.Video.Colour.Primaries != 9 || video.Video.Colour.TransferCharacterist != 16 {
		t.Fatalf("colour = %+v", video.Video.Colour)
	}
	audio := a.TrackByNumber(3)
	if audio == nil || audio.Audio == nil {
		t.Fatal("audio track metadata missing")
	}
	if audio.Audio.Channels != 6 || audio.Audio.SamplingFrequency != 48000 {
		t.Fatalf("audio = %+v", audio.Audio)
	}
	if audio.Audio.Layout != tracks.Layout5_1 {
		t.Fatalf("layout = %q", audio.Audio.Layout)
	}
	if len(a.Fragments) != 10 {
		t.Fatalf("fragments = %d", len(a.Fragments))
	}
}

func TestFragmentsKeepInterleavedDocumentOrder(t *testing.T) {
	dir := fixtures(t)
	a, err := Parse(filepath.Join(dir, testdata.ValidMKV))
	if err != nil {
		t.Fatal(err)
	}
	// Tracks arrive 1,2,3,4,5 per cluster.
	perCluster := map[int][]uint64{}
	for _, f := range a.Fragments {
		perCluster[f.Cluster] = append(perCluster[f.Cluster], f.TrackNumber)
	}
	want := []uint64{1, 2, 3, 4, 5}
	for c, order := range perCluster {
		if len(order) != len(want) {
			t.Fatalf("cluster %d has %d blocks", c, len(order))
		}
		for i := range want {
			if order[i] != want[i] {
				t.Fatalf("cluster %d order = %v", c, order)
			}
		}
	}
	// Order is globally increasing, proving document order was preserved.
	for i := 1; i < len(a.Fragments); i++ {
		if a.Fragments[i].Order != a.Fragments[i-1].Order+1 {
			t.Fatalf("fragment order not contiguous at %d", i)
		}
	}
}

func TestTimecodeScaleConversion(t *testing.T) {
	dir := fixtures(t)
	a, err := Parse(filepath.Join(dir, testdata.ValidMKV))
	if err != nil {
		t.Fatal(err)
	}
	// Cluster 1 has Timecode 1000 ticks * 1 ms = 1 second.
	var second []media.Fragment
	for _, f := range a.Fragments {
		if f.Cluster == 1 {
			second = append(second, f)
		}
	}
	if len(second) == 0 {
		t.Fatal("no fragments in second cluster")
	}
	for _, f := range second {
		if f.Start != 1_000_000_000 {
			t.Fatalf("track %d start = %d, want 1e9", f.TrackNumber, f.Start)
		}
		if f.Reference != 1_000_000_000 {
			t.Fatalf("reference = %d", f.Reference)
		}
	}
}

func TestUnknownLengthMastersParseIdentically(t *testing.T) {
	dir := fixtures(t)
	known, err := Parse(filepath.Join(dir, testdata.ValidMKV))
	if err != nil {
		t.Fatal(err)
	}
	unknown, err := Parse(filepath.Join(dir, testdata.UnknownSizeMKV))
	if err != nil {
		t.Fatalf("unknown-size parse: %v", err)
	}
	if unknown.UnknownSized <= known.UnknownSized {
		t.Fatalf("unknown-sized masters = %d, known fixture = %d",
			unknown.UnknownSized, known.UnknownSized)
	}
	if len(unknown.Tracks) != len(known.Tracks) {
		t.Fatalf("tracks differ: %d vs %d", len(unknown.Tracks), len(known.Tracks))
	}
	if len(unknown.Fragments) != len(known.Fragments) {
		t.Fatalf("fragments differ: %d vs %d", len(unknown.Fragments), len(known.Fragments))
	}
	if rep := fragments.Validate(unknown); !rep.OK() {
		t.Fatalf("unknown-size fragments rejected: %+v", rep.Problems)
	}
}

func TestHeaderFailuresCarryOffsets(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		cat  timeline.Category
		off  int64
	}{
		// An empty file has no header at all, which is a missing element rather
		// than a truncation of one that started.
		{"empty", nil, timeline.CatMissingElement, 0},
		// A 1-byte EBML header payload closes cleanly but has no DocType.
		{"no doctype", []byte{0x1A, 0x45, 0xDF, 0xA3, 0x80}, timeline.CatMissingElement, 0},
		// 0xFF is a one-byte ID whose size vint is the unknown-size sentinel; it
		// is not a master, so an unknown size here is illegal.
		{"garbage", []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF}, timeline.CatUnknownLength, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "bad.mkv")
			if err := os.WriteFile(path, tc.data, 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := Parse(path)
			if err == nil {
				t.Fatal("malformed file accepted")
			}
			if timeline.CategoryOf(err) != tc.cat {
				t.Fatalf("category = %s, want %s (%v)", timeline.CategoryOf(err), tc.cat, err)
			}
			if got := timeline.OffsetOf(err); got != tc.off {
				t.Fatalf("offset = %d, want %d", got, tc.off)
			}
		})
	}
}

func TestUnsupportedDocType(t *testing.T) {
	// A well-formed EBML header whose DocType is not matroska or webm.
	header := testdata.HeaderForTest("avi")
	path := filepath.Join(t.TempDir(), "other.mkv")
	if err := os.WriteFile(path, header, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Parse(path)
	if timeline.CategoryOf(err) != timeline.CatUnsupported {
		t.Fatalf("category = %s", timeline.CategoryOf(err))
	}
}
