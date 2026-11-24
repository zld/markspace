package fixture

import (
	"os"
	"path/filepath"
	"testing"

	"mediarun/internal/manifest"
	"mediarun/internal/parse"
)

func TestGenerateSmoke(t *testing.T) {
	dir := t.TempDir()
	if err := Generate(dir); err != nil {
		t.Fatal(err)
	}
	// MPD parses with 2 reps x 5 segments.
	mpdData, err := os.ReadFile(filepath.Join(dir, "manifest.mpd"))
	if err != nil {
		t.Fatal(err)
	}
	mpd, err := manifest.ParseMPD("manifest.mpd", mpdData)
	if err != nil {
		t.Fatal(err)
	}
	if len(mpd.Representations) != 2 || len(mpd.Representations[0].Segments) != 5 {
		t.Fatalf("bad mpd: %+v", mpd.Representations)
	}
	// Init and segment parse.
	initData, _ := os.ReadFile(filepath.Join(dir, "init-v1.mp4"))
	tracks, err := parse.ParseInit("init-v1.mp4", initData)
	if err != nil || tracks[1].Timescale != 90000 {
		t.Fatalf("init: %v %+v", err, tracks)
	}
	segData, _ := os.ReadFile(filepath.Join(dir, "seg-v1-1.m4s"))
	seg, err := parse.ParseMediaSegment("seg-v1-1.m4s", segData)
	if err != nil {
		t.Fatal(err)
	}
	if len(seg.Fragments[0].Samples) != 60 || seg.MediaBytes != 300000 {
		t.Fatalf("segment: %+v media=%d", seg.Fragments[0].Samples[0], seg.MediaBytes)
	}
	// HLS playlist and TS parse.
	plData, _ := os.ReadFile(filepath.Join(dir, "playlist.m3u8"))
	pl, err := manifest.ParsePlaylist("playlist.m3u8", plData)
	if err != nil {
		t.Fatal(err)
	}
	if len(pl.Segments) != 4 || pl.Discontinuities != 1 {
		t.Fatalf("playlist: %+v", pl)
	}
	tsData, _ := os.ReadFile(filepath.Join(dir, "seg2.ts"))
	ts, err := parse.ParseTS("seg2.ts", tsData)
	if err != nil {
		t.Fatal(err)
	}
	if ts.FirstPTS != 9000 {
		t.Fatalf("seg2 first PTS = %d, want 9000 (post-discontinuity jump)", ts.FirstPTS)
	}
	// Corrupt fixtures exist.
	for _, sub := range []string{"truncated", "zerots", "dupts"} {
		if _, err := os.Stat(filepath.Join(dir, "corrupt", sub, "manifest.mpd")); err != nil {
			t.Fatalf("missing corrupt fixture %s: %v", sub, err)
		}
	}
}
