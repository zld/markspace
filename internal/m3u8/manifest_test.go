package m3u8

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestMediaRoundTrip(t *testing.T) {
	orig := MediaPlaylist{
		Version:        3,
		TargetDuration: 3 * time.Second,
		MediaSequence:  5,
		PlaylistType:   "VOD",
		Endlist:        true,
		Segments: []Segment{
			{URI: "seg0.ts", Duration: 3000 * time.Millisecond},
			{URI: "seg1.ts", Duration: 2980 * time.Millisecond},
			{URI: "seg2.ts", Duration: 3020 * time.Millisecond},
		},
	}
	var buf bytes.Buffer
	if err := WriteMedia(&buf, orig); err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseMedia(&buf)
	if err != nil {
		t.Fatalf("parse:\n%s\n%v", buf.String(), err)
	}
	if parsed.MediaSequence != 5 || !parsed.Endlist ||
		parsed.PlaylistType != "VOD" || parsed.TargetDuration != 3*time.Second {
		t.Fatalf("header mismatch: %+v", parsed)
	}
	if len(parsed.Segments) != 3 {
		t.Fatalf("segments: %d", len(parsed.Segments))
	}
	for i, s := range parsed.Segments {
		if s.Sequence != 5+uint64(i) {
			t.Fatalf("segment %d sequence: %d", i, s.Sequence)
		}
		if s.URI != orig.Segments[i].URI {
			t.Fatalf("uri: %s", s.URI)
		}
		if diff := s.Duration - orig.Segments[i].Duration; diff > 5*time.Millisecond ||
			diff < -5*time.Millisecond {
			t.Fatalf("segment %d duration drift: %v", i, diff)
		}
	}
	if got := parsed.DurationMS(); got != 9000 {
		t.Fatalf("total duration ms: %d", got)
	}
}

func TestMasterRoundTrip(t *testing.T) {
	orig := MasterPlaylist{
		Version: 3,
		Variants: []Variant{
			{URI: "1800k/index.m3u8", Bandwidth: 1_800_000, Codecs: `"avc1.640028,mp4a.40.2"`},
			{URI: "450k/index.m3u8", Bandwidth: 450_000, Resolution: "640x360"},
		},
	}
	var buf bytes.Buffer
	if err := WriteMaster(&buf, orig); err != nil {
		t.Fatal(err)
	}
	// Variants must be emitted sorted by bandwidth.
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if !strings.Contains(lines[2], "BANDWIDTH=450000") {
		t.Fatalf("expected low bitrate first:\n%s", buf.String())
	}
	parsed, err := ParseMaster(&buf)
	if err != nil {
		t.Fatalf("parse:\n%s\n%v", buf.String(), err)
	}
	if len(parsed.Variants) != 2 {
		t.Fatalf("variants: %d", len(parsed.Variants))
	}
	if parsed.Variants[0].Bandwidth != 450_000 ||
		parsed.Variants[1].Bandwidth != 1_800_000 {
		t.Fatalf("ordering: %+v", parsed.Variants)
	}
	if parsed.Variants[1].Codecs == "" {
		t.Fatal("codecs attribute lost")
	}
}

func TestParseMediaErrors(t *testing.T) {
	if _, err := ParseMedia(strings.NewReader("not-a-playlist\n")); err == nil {
		t.Fatal("missing header must error")
	}
	dangling := "#EXTM3U\n#EXTINF:3.0,\n"
	if _, err := ParseMedia(strings.NewReader(dangling)); err == nil {
		t.Fatal("dangling EXTINF must error")
	}
	badInf := "#EXTM3U\n#EXTINF:abc,\nseg.ts\n"
	if _, err := ParseMedia(strings.NewReader(badInf)); err == nil {
		t.Fatal("bad EXTINF must error")
	}
}

func TestParseMasterMissingBandwidth(t *testing.T) {
	in := "#EXTM3U\n#EXT-X-STREAM-INF:CODECS=\"x\"\nidx.m3u8\n"
	if _, err := ParseMaster(strings.NewReader(in)); err == nil {
		t.Fatal("missing BANDWIDTH must error")
	}
}

func TestQuotedAttributesWithCommas(t *testing.T) {
	attrs, err := parseAttributes(`BANDWIDTH=900000,CODECS="avc1.4d401f,mp4a.40.2",RESOLUTION=1280x720`)
	if err != nil {
		t.Fatal(err)
	}
	if attrs["CODECS"] != `"avc1.4d401f,mp4a.40.2"` {
		t.Fatalf("codecs: %q", attrs["CODECS"])
	}
	if attrs["RESOLUTION"] != "1280x720" || attrs["BANDWIDTH"] != "900000" {
		t.Fatalf("attrs: %+v", attrs)
	}
}

func TestCanonicalOutputStable(t *testing.T) {
	m := MediaPlaylist{TargetDuration: 3 * time.Second,
		Segments: []Segment{{URI: "a.ts", Duration: 3 * time.Second}}}
	var b1, b2 bytes.Buffer
	WriteMedia(&b1, m)
	WriteMedia(&b2, m)
	if b1.String() != b2.String() {
		t.Fatal("serialisation is not deterministic")
	}
}
