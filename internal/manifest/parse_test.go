package manifest

import (
	"strings"
	"testing"
)

func TestParseRejectsBrokenPlaylists(t *testing.T) {
	valid := string(Render(Playlist{Version: 3, TargetDuration: 4, Segments: []PlaylistSegment{
		{FileName: "a.ts", DurationMS: 4000},
	}}))
	cases := map[string]string{
		"no endlist":      strings.Replace(valid, "#EXT-X-ENDLIST\n", "", 1),
		"no header":       strings.Replace(valid, "#EXTM3U\n", "", 1),
		"unknown tag":     strings.Replace(valid, "#EXT-X-ENDLIST", "#EXT-X-FOO:1", 1),
		"dup file":        strings.Replace(valid, "#EXT-X-ENDLIST", "#EXTINF:4.000,\na.ts\n#EXT-X-ENDLIST", 1),
		"bad precision":   strings.Replace(valid, "4.000", "4.0", 1),
		"missing target":  strings.Replace(valid, "#EXT-X-TARGETDURATION:4\n", "", 1),
		"target mismatch": strings.Replace(valid, "TARGETDURATION:4", "TARGETDURATION:9", 1),
		"orphan segment":  strings.Replace(valid, "#EXTINF:4.000,\n", "", 1),
		"dangling extinf": strings.Replace(valid, "a.ts\n", "", 1),
	}
	for name, data := range cases {
		if _, err := Parse([]byte(data)); err == nil {
			t.Errorf("%s: Parse succeeded, want error", name)
		}
	}
}

func TestRenderParseRoundTrip(t *testing.T) {
	p, err := BuildPlaylist(800, sampleSegments())
	if err != nil {
		t.Fatalf("BuildPlaylist: %v", err)
	}
	if p.MediaSequence != 0 || p.TargetDuration != 5 || len(p.Segments) != 3 {
		t.Fatalf("playlist = %+v", p)
	}
	if p.Segments[1].ID != 1 || p.Segments[2].ID != 2 {
		t.Fatalf("segments not sorted: %+v", p.Segments)
	}
	text := string(Render(p))
	want := "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:5\n" +
		"#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-PLAYLIST-TYPE:VOD\n" +
		"#EXT-X-INDEPENDENT-SEGMENTS\n#EXTINF:4.000,\nseg_800_0.ts\n" +
		"#EXTINF:3.999,\nseg_800_1.ts\n#EXTINF:4.001,\nseg_800_2.ts\n#EXT-X-ENDLIST\n"
	if text != want {
		t.Fatalf("Render:\n%s\nwant:\n%s", text, want)
	}

	back, err := Parse([]byte(text))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if back.TargetDuration != p.TargetDuration || len(back.Segments) != len(p.Segments) {
		t.Fatalf("round trip = %+v, want %+v", back, p)
	}
	for i := range back.Segments {
		if back.Segments[i] != p.Segments[i] {
			t.Fatalf("segment %d = %+v, want %+v", i, back.Segments[i], p.Segments[i])
		}
	}
}
