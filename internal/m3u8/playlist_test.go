package m3u8

import (
	"bytes"
	"strings"
	"testing"
)

func TestMediaRoundTrip(t *testing.T) {
	src := MediaPlaylist{
		TargetDuration: 2,
		MediaSequence:  7,
		Closed:         true,
		Segments: []Segment{
			{URI: "seg-7.ts", Duration: 2.0, MediaSeq: 7},
			{URI: "seg-8.ts", Duration: 1.98, Discontinuity: true, MediaSeq: 8},
		},
	}
	parsed, err := RoundTrip(src)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.TargetDuration != 2 || parsed.MediaSequence != 7 || !parsed.Closed {
		t.Fatalf("header mismatch: %+v", parsed)
	}
	if len(parsed.Segments) != 2 {
		t.Fatalf("segments = %d", len(parsed.Segments))
	}
	if !parsed.Segments[1].Discontinuity {
		t.Fatal("discontinuity lost in round trip")
	}
	if parsed.Segments[1].Duration < 1.97 || parsed.Segments[1].Duration > 1.99 {
		t.Fatalf("duration = %f", parsed.Segments[1].Duration)
	}
	if parsed.MediaSeqOf(1) != 8 {
		t.Fatalf("media seq = %d", parsed.MediaSeqOf(1))
	}
	if !strings.Contains(string(src.Bytes()), "#EXT-X-ENDLIST") {
		t.Fatal("closed playlist missing ENDLIST")
	}
}

func TestEventPlaylistOpen(t *testing.T) {
	src := MediaPlaylist{TargetDuration: 4, Segments: []Segment{{URI: "a.ts", Duration: 4}}}
	text := string(src.Bytes())
	if strings.Contains(text, "ENDLIST") {
		t.Fatal("open playlist must not contain ENDLIST")
	}
	if !strings.Contains(text, "EVENT") {
		t.Fatal("open playlist should be typed EVENT")
	}
}

func TestMasterRoundTrip(t *testing.T) {
	m := MasterPlaylist{Variants: []Variant{
		{URI: "1080p/index.m3u8", Bandwidth: 3500000, Name: "1080p", Codecs: "avc1.4d401f,mp4a.40.2"},
		{URI: "720p/index.m3u8", Bandwidth: 1800000, Name: "720p"},
	}}
	parsed, err := ParseMaster(bytes.NewReader(m.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	// Output is bandwidth sorted: 720p first.
	if len(parsed) != 2 || parsed[0].Bandwidth != 1800000 {
		t.Fatalf("variants = %+v", parsed)
	}
	if parsed[1].Codecs == "" || parsed[1].Name != "1080p" {
		t.Fatalf("attrs lost: %+v", parsed[1])
	}
}

func TestParseErrors(t *testing.T) {
	if _, err := ParseMedia(strings.NewReader("garbage\n")); err == nil {
		t.Fatal("expected header error")
	}
	bad := "#EXTM3U\n#EXT-X-TARGETDURATION:x\n"
	if _, err := ParseMedia(strings.NewReader(bad)); err == nil {
		t.Fatal("expected integer parse error")
	}
	noInf := "#EXTM3U\nseg.ts\n"
	if _, err := ParseMedia(strings.NewReader(noInf)); err == nil {
		t.Fatal("expected EXTINF error")
	}
	if _, err := ParseMaster(strings.NewReader("#EXTM3U\nuri.ts\n")); err == nil {
		t.Fatal("expected STREAM-INF error")
	}
}
