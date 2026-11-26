package manifest

import (
	"path/filepath"
	"testing"

	"continuity/internal/clock"
)

const hlsOK = `#EXTM3U
#EXT-X-VERSION:6
#EXT-X-TARGETDURATION:2
#EXT-X-MEDIA-SEQUENCE:10
#EXTINF:2.0,
seg-10.m4s
#EXTINF:2.0,
seg-11.m4s
#EXT-X-DISCONTINUITY
#EXTINF:2.0,
seg-12.m4s
#EXTINF:2.0,
seg-13.m4s
#EXT-X-ENDLIST
`

func hlsFiles(m3u8 string) map[string]string {
	files := map[string]string{"pl.m3u8": m3u8}
	for n := 10; n <= 13; n++ {
		files["seg-"+itoa(n)+".m4s"] = "media-bytes"
	}
	return files
}

func TestParseHLSOk(t *testing.T) {
	dir := writeTree(t, hlsFiles(hlsOK))
	m, err := ParseHLS(filepath.Join(dir, "pl.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	rep := m.Representations[0]
	if len(rep.Segments) != 4 {
		t.Fatalf("segments = %d", len(rep.Segments))
	}
	if len(rep.Discontinuities) != 1 || rep.Discontinuities[0] != 2 {
		t.Fatalf("discontinuities = %v", rep.Discontinuities)
	}
	if rep.Segments[0].Sequence != 10 || rep.Segments[3].Sequence != 13 {
		t.Fatalf("sequences wrong: %d..%d", rep.Segments[0].Sequence, rep.Segments[3].Sequence)
	}
	if c, _ := rep.Segments[1].Start.Cmp(clock.MustR(2, 1)); c != 0 {
		t.Errorf("start[1] = %s", rep.Segments[1].Start)
	}
	if rep.Segments[0].Line != 5 {
		t.Errorf("first EXTINF line = %d, want 5", rep.Segments[0].Line)
	}
	if c, _ := m.MediaPresentSec.Cmp(clock.MustR(8, 1)); c != 0 {
		t.Errorf("total = %s", m.MediaPresentSec)
	}
}

func TestParseHLSDuplicate(t *testing.T) {
	bad := `#EXTM3U
#EXT-X-TARGETDURATION:2
#EXT-X-MEDIA-SEQUENCE:1
#EXTINF:2.0,
a.m4s
#EXTINF:2.0,
a.m4s
#EXT-X-ENDLIST
`
	dir := writeTree(t, map[string]string{"pl.m3u8": bad, "a.m4s": "x"})
	_, err := ParseHLS(filepath.Join(dir, "pl.m3u8"))
	me := clock.AsMedia(err)
	if me == nil || me.Kind != clock.KindInvalidInput || me.Line == 0 {
		t.Fatalf("want line-located duplicate error, got %+v", err)
	}
}

func TestParseHLSRejectsMaster(t *testing.T) {
	master := `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=100000
child.m3u8
`
	dir := writeTree(t, map[string]string{"master.m3u8": master})
	_, err := ParseHLS(filepath.Join(dir, "master.m3u8"))
	me := clock.AsMedia(err)
	if me == nil || me.Kind != clock.KindInvalidInput {
		t.Fatalf("want master rejection, got %v", err)
	}
}

func TestParseHLSBadHeader(t *testing.T) {
	dir := writeTree(t, map[string]string{"pl.m3u8": "NOTM3U\n"})
	_, err := ParseHLS(filepath.Join(dir, "pl.m3u8"))
	if clock.AsMedia(err) == nil {
		t.Fatalf("want structured error, got %v", err)
	}
}

func TestParseHLSURIBeforeEXTINF(t *testing.T) {
	bad := "#EXTM3U\nseg.m4s\n"
	dir := writeTree(t, map[string]string{"pl.m3u8": bad, "seg.m4s": "x"})
	_, err := ParseHLS(filepath.Join(dir, "pl.m3u8"))
	if clock.AsMedia(err) == nil {
		t.Fatalf("want dangling-URI error, got %v", err)
	}
}
