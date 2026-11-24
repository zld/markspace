package manifest

import (
	"errors"
	"strings"
	"testing"

	"mediarun/internal/clock"
	"mediarun/internal/fault"
)

const goodPlaylist = `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:2
#EXT-X-MEDIA-SEQUENCE:0
#EXTINF:2.000,
seg0.ts
#EXTINF:2.000,
seg1.ts
#EXT-X-DISCONTINUITY
#EXTINF:2.000,
seg2.ts
#EXTINF:1.500,
seg3.ts
#EXT-X-ENDLIST
`

func TestParsePlaylistGood(t *testing.T) {
	pl, err := ParsePlaylist("index.m3u8", []byte(goodPlaylist))
	if err != nil {
		t.Fatal(err)
	}
	if len(pl.Segments) != 4 {
		t.Fatalf("got %d segments", len(pl.Segments))
	}
	if pl.Discontinuities != 1 {
		t.Fatalf("got %d discontinuities", pl.Discontinuities)
	}
	// Unified timeline: 2+2+2+1.5 s = 7.5 s = 675000 ticks.
	if got := pl.Segments[3].Start + pl.Segments[3].Duration; got != 675000 {
		t.Fatalf("timeline end = %d", got)
	}
	if pl.Segments[2].DiscSeq != 1 || pl.Segments[0].DiscSeq != 0 {
		t.Fatalf("bad discontinuity seq: %+v", pl.Segments)
	}
	if pl.Segments[1].Start != clock.Ticks(180000) {
		t.Fatalf("seg1 start = %d", pl.Segments[1].Start)
	}
	for _, s := range pl.Segments {
		if s.Offset < 0 || s.URI == "" {
			t.Fatalf("segment missing offset/uri: %+v", s)
		}
	}
}

func TestParsePlaylistIntegerAccumulation(t *testing.T) {
	// 100 segments of 9.976 s must sum exactly, no float drift.
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-TARGETDURATION:10\n")
	for i := 0; i < 100; i++ {
		b.WriteString("#EXTINF:9.976,\nseg.ts\n")
	}
	pl, err := ParsePlaylist("p.m3u8", []byte(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	last := pl.Segments[99]
	if got := last.Start + last.Duration; got != 89784000 {
		t.Fatalf("timeline end = %d, want 89784000", got)
	}
}

func TestParsePlaylistBadEXTINF(t *testing.T) {
	bad := strings.Replace(goodPlaylist, "#EXTINF:2.000,", "#EXTINF:abc,", 1)
	_, err := ParsePlaylist("p.m3u8", []byte(bad))
	var fe *fault.Error
	if !errors.As(err, &fe) {
		t.Fatalf("got %v", err)
	}
	if fe.Stage != fault.StageManifest || fe.Offset <= 0 || fe.Entry != 0 {
		t.Fatalf("bad fault: %+v", fe)
	}
}

func TestParsePlaylistDanglingEXTINF(t *testing.T) {
	bad := "#EXTM3U\n#EXTINF:2.0,\n"
	_, err := ParsePlaylist("p.m3u8", []byte(bad))
	if err == nil || !strings.Contains(err.Error(), "without following URI") {
		t.Fatalf("got %v", err)
	}
}

func TestParsePlaylistNoHeader(t *testing.T) {
	_, err := ParsePlaylist("p.m3u8", []byte("#EXTINF:2.0,\na.ts\n"))
	if err == nil || !strings.Contains(err.Error(), "EXTM3U") {
		t.Fatalf("got %v", err)
	}
}
