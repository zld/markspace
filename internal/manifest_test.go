package continuity

import (
	"strings"
	"testing"
)

const sampleHLS = `#EXTM3U
#EXT-X-VERSION:6
#EXT-X-TARGETDURATION:2
#EXT-X-MEDIA-SEQUENCE:10
#EXT-X-MAP:URI="init.mp4"
#EXTINF:2.0,
seg10.m4s
#EXTINF:2.0,
seg11.m4s
`

func TestParseHLSBasic(t *testing.T) {
	m, err := ParseHLS("main/index.m3u8", []byte(sampleHLS))
	if err != nil {
		t.Fatal(err)
	}
	if m.Kind != ManifestHLS {
		t.Fatalf("kind=%s", m.Kind)
	}
	if m.TargetDuration != 2*Ticks(BaseTimescale) {
		t.Fatalf("target=%d", m.TargetDuration)
	}
	if m.MediaSequence != 10 {
		t.Fatalf("media sequence=%d", m.MediaSequence)
	}
	segs := m.Segments()
	if len(segs) != 2 {
		t.Fatalf("segments=%d", len(segs))
	}
	if segs[0].URI != "seg10.m4s" || segs[0].InitURI != "init.mp4" {
		t.Fatalf("seg0=%+v", segs[0])
	}
	if segs[0].Start != 0 || segs[1].Start != 2*Ticks(BaseTimescale) {
		t.Fatalf("starts=%d,%d", segs[0].Start, segs[1].Start)
	}
	if segs[1].Number != 11 || segs[1].Line != 9 {
		t.Fatalf("seg1 number/line=%d/%d", segs[1].Number, segs[1].Line)
	}
}

func TestParseHLSByteRangeContinuation(t *testing.T) {
	pl := `#EXTM3U
#EXT-X-TARGETDURATION:4
#EXTINF:4.0,
#EXT-X-BYTERANGE:1000@0
all.mp4
#EXTINF:4.0,
#EXT-X-BYTERANGE:500
all.mp4
`
	m, err := ParseHLS("p.m3u8", []byte(pl))
	if err != nil {
		t.Fatal(err)
	}
	segs := m.Segments()
	if segs[0].ByteRange == nil || segs[0].ByteRange.Start != 0 || segs[0].ByteRange.End != 1000 {
		t.Fatalf("range0=%+v", segs[0].ByteRange)
	}
	if segs[1].ByteRange == nil || segs[1].ByteRange.Start != 1000 || segs[1].ByteRange.End != 1500 {
		t.Fatalf("range1=%+v", segs[1].ByteRange)
	}
}

func TestParseHLSRejectsPathEscape(t *testing.T) {
	cases := []string{
		"../../etc/passwd",
		"seg/../../escape.m4s",
		"/abs/seg.m4s",
		"http://example.com/seg.m4s",
		"seg%2F..%2Fx.m4s",
		"seg\\win.m4s",
		"seg.m4s?token=1",
	}
	for _, ref := range cases {
		pl := "#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXTINF:2.0,\n" + ref + "\n"
		_, err := ParseHLS("p.m3u8", []byte(pl))
		if err == nil {
			t.Errorf("ref %q unexpectedly accepted", ref)
			continue
		}
		if !IsKind(err, KindPathEscape) && !IsKind(err, KindParse) {
			t.Errorf("ref %q wrong kind: %v", ref, err)
		}
		if loc := LocationOf(err); loc.Line != 4 {
			t.Errorf("ref %q line=%d want 4", ref, loc.Line)
		}
	}
}

func TestParseHLSFailuresCarryLine(t *testing.T) {
	cases := []struct {
		name string
		pl   string
		kind Kind
		line int
	}{
		{"bad header", "not a playlist\n", KindParse, 1},
		{"missing target", "#EXTM3U\n#EXTINF:1.0,\na.m4s\n", KindParse, 4},
		{"uri without extinf", "#EXTM3U\n#EXT-X-TARGETDURATION:2\norphan.m4s\n", KindParse, 3},
		{"bad extinf", "#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXTINF:abc,\na.m4s\n", KindParse, 3},
		{"dangling extinf", "#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXTINF:2.0,\n", KindParse, 3},
		{"master playlist", "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nv.m3u8\n", KindUnsupported, 2},
		{"empty", "#EXTM3U\n#EXT-X-TARGETDURATION:2\n", KindParse, 3},
	}
	for _, tc := range cases {
		_, err := ParseHLS("p.m3u8", []byte(tc.pl))
		if err == nil {
			t.Errorf("%s: expected failure", tc.name)
			continue
		}
		if !IsKind(err, tc.kind) {
			t.Errorf("%s: kind=%v want %v", tc.name, err, tc.kind)
		}
		if loc := LocationOf(err); loc.Line != tc.line {
			t.Errorf("%s: line=%d want %d", tc.name, loc.Line, tc.line)
		}
	}
}

func TestResolveRef(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		base, ref, want string
		ok              bool
	}{
		{"main/index.m3u8", "seg.m4s", "main/seg.m4s", true},
		{"main/index.m3u8", "../alt/seg.m4s", "alt/seg.m4s", true},
		{"main/index.m3u8", "../../escape.m4s", "", false},
		{"index.m3u8", "../escape.m4s", "", false},
		{"main/index.m3u8", "/etc/passwd", "", false},
	}
	for _, tc := range cases {
		got, err := ResolveRef(root, tc.base, tc.ref)
		if tc.ok && err != nil {
			t.Errorf("%q: %v", tc.ref, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("%q: expected rejection", tc.ref)
		}
		if tc.ok && got != tc.want {
			t.Errorf("%q -> %q want %q", tc.ref, got, tc.want)
		}
	}
}

func TestValidateRelativeRefAllowsNested(t *testing.T) {
	for _, ok := range []string{"a.m4s", "video/seg-1.m4s", "seg%20name.m4s"} {
		if err := validateRelativeRef(ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
}

func TestParseHLSDiscontinuity(t *testing.T) {
	pl := `#EXTM3U
#EXT-X-TARGETDURATION:2
#EXT-X-DISCONTINUITY-SEQUENCE:3
#EXTINF:2.0,
a.m4s
#EXT-X-DISCONTINUITY
#EXTINF:2.0,
b.m4s
`
	m, err := ParseHLS("p.m3u8", []byte(pl))
	if err != nil {
		t.Fatal(err)
	}
	segs := m.Segments()
	if segs[0].Discontinuity || segs[0].DiscSequence != 3 {
		t.Fatalf("seg0=%+v", segs[0])
	}
	if !segs[1].Discontinuity || segs[1].DiscSequence != 4 {
		t.Fatalf("seg1=%+v", segs[1])
	}
}

func TestParseHLSRejectsOversized(t *testing.T) {
	big := strings.Repeat("# comment\n", 100)
	_, err := ParseHLS("p.m3u8", []byte(big))
	if err == nil || !IsKind(err, KindParse) {
		t.Fatalf("expected parse failure, got %v", err)
	}
}
