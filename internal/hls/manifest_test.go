package hls_test

import (
	"bytes"
	"strings"
	"testing"

	"hlscheck/internal/hls"
)

func sample() *hls.Playlist {
	return hls.New(0, []hls.Segment{
		{Seq: 0, URI: "seg_00000.ts", Duration: 4.0, SHA256: strings.Repeat("a", 64)},
		{Seq: 1, URI: "seg_00001.ts", Duration: 3.96, SHA256: strings.Repeat("b", 64)},
	})
}

func TestRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := hls.Encode(&buf, sample()); err != nil {
		t.Fatal(err)
	}
	p, err := hls.Decode(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if p.TargetDuration != 4 || p.MediaSequence != 0 || !p.EndList {
		t.Errorf("bad header: %+v", p)
	}
	if len(p.Segments) != 2 || p.Segments[1].Duration != 3.96 {
		t.Errorf("bad segments: %+v", p.Segments)
	}
	if p.Segments[0].SHA256 != strings.Repeat("a", 64) {
		t.Errorf("sha mismatch: %q", p.Segments[0].SHA256)
	}
	if got := p.TotalDuration(); got != 7.96 {
		t.Errorf("total = %v, want 7.96", got)
	}
}

func TestRejectDuplicateSeq(t *testing.T) {
	m := "#EXTM3U\n#EXT-X-TARGETDURATION:4\n" +
		hls.TagSeq + "0\n#EXTINF:4.0,\n" + hls.TagSHA256 + strings.Repeat("a", 64) + "\na.ts\n" +
		hls.TagSeq + "0\n#EXTINF:4.0,\n" + hls.TagSHA256 + strings.Repeat("b", 64) + "\nb.ts\n"
	if _, err := hls.Decode(strings.NewReader(m)); err == nil ||
		!strings.Contains(err.Error(), "duplicate segment sequence") {
		t.Errorf("want duplicate seq error, got %v", err)
	}
}

func TestRejectDuplicateMediaSequence(t *testing.T) {
	m := "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-MEDIA-SEQUENCE:1\n"
	if _, err := hls.Decode(strings.NewReader(m)); err == nil ||
		!strings.Contains(err.Error(), "duplicate EXT-X-MEDIA-SEQUENCE") {
		t.Errorf("want duplicate media sequence error, got %v", err)
	}
}

func TestRejectIllegalDuration(t *testing.T) {
	for _, dur := range []string{"-1.0", "abc", "NaN", "+Inf", "99999.0"} {
		m := "#EXTM3U\n" + hls.TagSeq + "0\n#EXTINF:" + dur + ",\n" +
			hls.TagSHA256 + strings.Repeat("a", 64) + "\na.ts\n"
		if _, err := hls.Decode(strings.NewReader(m)); err == nil ||
			!strings.Contains(err.Error(), "illegal EXTINF") {
			t.Errorf("duration %q: want illegal EXTINF error, got %v", dur, err)
		}
	}
}

func TestRejectOutOfBoundsPath(t *testing.T) {
	for _, uri := range []string{"../evil.ts", "/abs/path.ts", "a/../../b.ts", `..\win.ts`} {
		m := "#EXTM3U\n" + hls.TagSeq + "0\n#EXTINF:4.0,\n" +
			hls.TagSHA256 + strings.Repeat("a", 64) + "\n" + uri + "\n"
		if _, err := hls.Decode(strings.NewReader(m)); err == nil ||
			!strings.Contains(err.Error(), "out-of-bounds") {
			t.Errorf("uri %q: want out-of-bounds error, got %v", uri, err)
		}
	}
}

func TestRejectMissingHeaderAndOrphanURI(t *testing.T) {
	if _, err := hls.Decode(strings.NewReader("#EXT-X-TARGETDURATION:4\n")); err == nil {
		t.Error("want missing #EXTM3U error")
	}
	m := "#EXTM3U\nseg_00000.ts\n"
	if _, err := hls.Decode(strings.NewReader(m)); err == nil ||
		!strings.Contains(err.Error(), "missing SEQ/EXTINF/SHA256") {
		t.Errorf("want orphan URI error, got %v", err)
	}
}
