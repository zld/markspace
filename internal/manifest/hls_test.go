package manifest

import (
	"testing"

	"mediabatch/internal/fault"
)

const sampleHLS = `#EXTM3U
#EXT-X-VERSION:7
#EXT-X-TARGETDURATION:4
#EXT-X-MEDIA-SEQUENCE:0
#EXTINF:4.000,
seg-1.m4s
#EXTINF:4.000,
seg-2.m4s
#EXT-X-DISCONTINUITY
#EXTINF:4.000,
seg-3.m4s
#EXTINF:4.000,
seg-4.m4s
#EXT-X-ENDLIST
`

func TestParseHLSBasic(t *testing.T) {
	m, err := ParseHLSBytes("hls/high.m3u8", []byte(sampleHLS))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Segments) != 4 {
		t.Fatalf("segments=%d", len(m.Segments))
	}
	if m.Segments[3].Start.String() != "12" || m.Segments[3].End.String() != "16" {
		t.Fatalf("range=%s..%s", m.Segments[3].Start, m.Segments[3].End)
	}
	disc := m.Discontinuities()
	if len(disc) != 1 || disc[0] != 2 {
		t.Fatalf("discontinuities=%v", disc)
	}
	if m.Segments[2].Line != 11 {
		t.Fatalf("line=%d", m.Segments[2].Line)
	}
	if got := m.Segments[2].Offset; got != int64(len("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:4\n#EXT-X-MEDIA-SEQUENCE:0\n#EXTINF:4.000,\nseg-1.m4s\n#EXTINF:4.000,\nseg-2.m4s\n#EXT-X-DISCONTINUITY\n#EXTINF:4.000,\n")) {
		t.Fatalf("offset=%d", got)
	}
	if m.Segments[2].URL != "seg-3.m4s" {
		t.Fatalf("url=%s", m.Segments[2].URL)
	}
	for _, i := range m.Issues {
		if i.Code != fault.CodeDiscontinuity || i.Source.Loc.Line != 9 {
			t.Fatalf("unexpected issue %+v", i)
		}
		if i.Severity != fault.SeverityWarning {
			t.Fatalf("discontinuity should be a warning, got %s", i.Severity)
		}
	}
}

func TestParseHLSDegrades(t *testing.T) {
	pl := `#EXTM3U
#EXT-X-TARGETDURATION:4
#EXTINF:4.000,
seg-1.m4s
#EXTINF:,
seg-2.m4s
#EXTINF:4.000,
seg-1.m4s
#EXT-X-CUSTOM-THING:1
#EXTINF:4.000,
seg-3.m4s
`
	m, err := ParseHLSBytes("hls/low.m3u8", []byte(pl))
	if err != nil {
		t.Fatal(err)
	}
	codes := map[fault.Code]int{}
	for _, i := range m.Issues {
		codes[i.Code]++
		if i.Source.Loc.File != "hls/low.m3u8" || i.Source.Loc.Line == 0 {
			t.Errorf("issue lacks locator: %+v", i)
		}
	}
	if codes[fault.CodeMissingDuration] != 1 {
		t.Fatalf("missing duration=%d issues=%+v", codes[fault.CodeMissingDuration], m.Issues)
	}
	if codes[fault.CodeDuplicateRef] != 1 {
		t.Fatalf("duplicate=%d", codes[fault.CodeDuplicateRef])
	}
	if codes[fault.CodeUnknownTag] != 1 {
		t.Fatalf("unknown tag=%d", codes[fault.CodeUnknownTag])
	}
	// The missing-duration URI is not published. The duplicate is isolated and
	// does not advance the timeline, so segment 3 keeps its declared position.
	if len(m.Segments) != 3 {
		t.Fatalf("segments=%d want 3", len(m.Segments))
	}
	if m.Segments[0].Isolated || !m.Segments[1].Isolated || m.Segments[2].Isolated {
		t.Fatalf("isolation flags: %v %v %v", m.Segments[0].Isolated, m.Segments[1].Isolated, m.Segments[2].Isolated)
	}
	if m.Segments[1].Start.String() != "4" || m.Segments[1].End.String() != "4" {
		t.Fatalf("duplicate range=%s..%s", m.Segments[1].Start, m.Segments[1].End)
	}
	if m.Segments[2].Start.String() != "4" || m.Segments[2].End.String() != "8" {
		t.Fatalf("timeline advanced incorrectly: %s..%s", m.Segments[2].Start, m.Segments[2].End)
	}
}

func TestParseHLSHeaderRequired(t *testing.T) {
	m, err := ParseHLSBytes("bad.m3u8", []byte("seg.m4s\n"))
	if err == nil {
		t.Fatal("expected error")
	}
	if m.Issues.Errors() == 0 || m.Issues[0].Source.Loc.Line != 1 {
		t.Fatalf("issues=%+v", m.Issues)
	}
}

func TestByteRange(t *testing.T) {
	n, off, err := ParseHLSSegmentRange("1024@4096")
	if err != nil || n != 1024 || off != 4096 {
		t.Fatalf("n=%d off=%d err=%v", n, off, err)
	}
	if _, _, err := ParseHLSSegmentRange("0@1"); err == nil {
		t.Fatal("expected zero-length error")
	}
	if _, _, err := ParseHLSSegmentRange("x"); err == nil {
		t.Fatal("expected parse error")
	}
}
