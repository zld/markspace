package hls

import (
	"strings"
	"testing"

	"hlscheck/internal/model"
)

func seg(seq int, durMS int64) model.Segment {
	return model.Segment{
		Seq: seq, File: "seg_" + string(rune('0'+seq)) + ".ts",
		StartPTS:        int64(seq) * model.ClockRate * 2,
		EndPTS:          int64(seq+1) * model.ClockRate * 2,
		DurationMSField: durMS,
	}
}

func TestTargetDurationCeil(t *testing.T) {
	segments := []model.Segment{seg(0, 2000), seg(1, 2000), seg(2, 1000)}
	if got := TargetDuration(segments); got != 2 {
		t.Fatalf("TargetDuration = %d, want 2", got)
	}
	fractional := []model.Segment{seg(0, 1500)}
	if got := TargetDuration(fractional); got != 2 {
		t.Fatalf("ceil TargetDuration = %d, want 2", got)
	}
}

func TestMediaPlaylistShape(t *testing.T) {
	ir := IndexRendition{
		Rendition: model.Rendition{Bps: 350000, Dir: "r"},
		Segments:  []model.Segment{seg(0, 2000), seg(1, 1000)},
		Playlist:  "r/playlist.m3u8",
	}
	got := string(MediaPlaylist(ir, 2))
	required := []string{
		"#EXTM3U", "#EXT-X-VERSION:3", "#EXT-X-TARGETDURATION:2",
		"#EXT-X-MEDIA-SEQUENCE:0", "#EXT-X-PLAYLIST-TYPE:VOD",
		"#EXTINF:2.000,", "#EXTINF:1.000,", "#EXT-X-ENDLIST",
	}
	for _, tag := range required {
		if !strings.Contains(got, tag) {
			t.Fatalf("playlist missing %q:\n%s", tag, got)
		}
	}
}

func TestMasterPlaylistOrdersByBandwidth(t *testing.T) {
	rends := []IndexRendition{
		{Rendition: model.Rendition{Bps: 800000}, Playlist: "hi/playlist.m3u8"},
		{Rendition: model.Rendition{Bps: 350000}, Playlist: "lo/playlist.m3u8"},
	}
	got := string(MasterPlaylist(rends))
	lo := strings.Index(got, "BANDWIDTH=350000")
	hi := strings.Index(got, "BANDWIDTH=800000")
	if lo < 0 || hi < 0 || lo > hi {
		t.Fatalf("bandwidth not ascending:\n%s", got)
	}
}

func TestPlaylistRegenerationIsStable(t *testing.T) {
	ir := IndexRendition{Segments: []model.Segment{seg(0, 2000)}, Playlist: "r/p.m3u8"}
	first := MediaPlaylist(ir, 2)
	second := MediaPlaylist(ir, 2)
	if string(first) != string(second) {
		t.Fatal("canonical playlist rendering is not deterministic")
	}
}
