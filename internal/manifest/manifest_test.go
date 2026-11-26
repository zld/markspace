package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"continuity/internal/media"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParseHLSMediaSequenceAndSegments(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "v0.m4s"), "x")
	writeFile(t, filepath.Join(root, "v1.m4s"), "y")
	pl := `#EXTM3U
#EXT-X-VERSION:7
#EXT-X-MEDIA-SEQUENCE:100
#EXTINF:1.0,
v0.m4s
#EXT-X-DISCONTINUITY
#EXTINF:1.5,
v1.m4s
`
	p := filepath.Join(root, "video.m3u8")
	writeFile(t, p, pl)
	m, err := ParseHLSPlaylist(p, root, "video")
	if err != nil {
		t.Fatal(err)
	}
	if m.MediaSequence != 100 || len(m.Segments) != 2 {
		t.Fatalf("seq=%d segs=%d", m.MediaSequence, len(m.Segments))
	}
	if m.Segments[0].Sequence != 100 || m.Segments[1].Sequence != 101 {
		t.Fatal("bad sequence expansion")
	}
	if !m.Segments[1].Discontinuity || m.Segments[1].StartNS != 1_000_000_000 {
		t.Fatalf("boundary/start wrong: %+v", m.Segments[1])
	}
}

func TestHLSRejectsTraversalDuplicateAndBadDuration(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "evil.m3u8")
	writeFile(t, p, "#EXTM3U\n#EXTINF:1,\n../../outside.m4s\n")
	_, err := ParseHLSPlaylist(p, root, "video")
	assertCode(t, err, media.CodePathEscape)

	writeFile(t, filepath.Join(root, "a.m4s"), "a")
	dup := filepath.Join(root, "dup.m3u8")
	writeFile(t, dup, "#EXTM3U\n#EXTINF:1,\na.m4s\n#EXTINF:1,\n./a.m4s\n")
	_, err = ParseHLSPlaylist(dup, root, "video")
	assertCode(t, err, media.CodeDuplicateRef)

	bad := filepath.Join(root, "bad.m3u8")
	writeFile(t, bad, "#EXTM3U\n#EXTINF:0,\na.m4s\n")
	_, err = ParseHLSPlaylist(bad, root, "video")
	assertCode(t, err, media.CodeIllegalDuration)
}

func TestParseDASHTimelineAndOverflow(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "v-1.m4s"), "x")
	mpd := `<MPD type="static"><Period><AdaptationSet contentType="video">
<Representation id="v"><SegmentTemplate timescale="90000" media="$RepresentationID$-$Number$.m4s" startNumber="1">
<SegmentTimeline><S t="0" d="45000" r="1"/></SegmentTimeline>
</SegmentTemplate></Representation></AdaptationSet></Period></MPD>`
	p := filepath.Join(root, "index.mpd")
	writeFile(t, p, mpd)
	b, err := ParseDASH(p, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Segments) != 2 || b.Segments[1].StartNS != 500_000_000 {
		t.Fatalf("bad timeline: %+v", b.Segments)
	}

	bad := strings.Replace(mpd, "45000", "18446744073709551615", 1)
	badPath := filepath.Join(root, "overflow.mpd")
	writeFile(t, badPath, bad)
	_, err = ParseDASH(badPath, root)
	f, ok := media.AsFault(err)
	if !ok || f.Code != media.CodeTimeOverflow || f.Offset == 0 {
		t.Fatalf("want overflow with offset, got %+v", err)
	}
}

func assertCode(t *testing.T, err error, code media.Code) {
	t.Helper()
	f, ok := media.AsFault(err)
	if !ok || f.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}
