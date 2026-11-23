package hls

import (
	"bytes"
	"testing"

	"mediadeliver/internal/model"
)

const discontinuityPlaylist = `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:4
#EXT-X-MEDIA-SEQUENCE:10
#EXT-X-DISCONTINUITY-SEQUENCE:2
#EXTINF:4.000,
seg10.m4s
#EXTINF:4.000,
seg11.m4s
#EXT-X-DISCONTINUITY
#EXTINF:2.500,
seg12.m4s
#EXTINF:2.500,
seg13.m4s
#EXT-X-ENDLIST
`

func TestDiscontinuityIntervals(t *testing.T) {
	p, err := Parse([]byte(discontinuityPlaylist), "disc.m3u8")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(p.Errors) != 0 {
		t.Fatalf("unexpected errors: %v", p.Errors)
	}
	if len(p.Segments) != 4 || len(p.Intervals) != 2 {
		t.Fatalf("segments=%d intervals=%d, want 4/2", len(p.Segments), len(p.Intervals))
	}
	if p.MediaSequence != 10 || p.DiscontinuitySeq != 2 {
		t.Fatalf("media seq=%d disc seq=%d", p.MediaSequence, p.DiscontinuitySeq)
	}
	iv0, iv1 := p.Intervals[0], p.Intervals[1]
	if iv0.DiscontinuitySeq != 2 || iv1.DiscontinuitySeq != 3 {
		t.Fatalf("interval discontinuity seq = %d/%d, want 2/3", iv0.DiscontinuitySeq, iv1.DiscontinuitySeq)
	}
	if iv0.StartNS != 0 || iv0.DurationNS != 8_000_000_000 {
		t.Fatalf("interval 0 = %+v", iv0)
	}
	if iv1.StartNS != 8_000_000_000 || iv1.DurationNS != 5_000_000_000 {
		t.Fatalf("interval 1 = %+v", iv1)
	}
	if !p.Segments[2].IsDiscontinuity {
		t.Fatal("first segment after discontinuity not flagged")
	}
	if p.Segments[2].IntervalOffsetNS != 0 || p.Segments[3].IntervalOffsetNS != 2_500_000_000 {
		t.Fatalf("interval offsets = %d/%d", p.Segments[2].IntervalOffsetNS, p.Segments[3].IntervalOffsetNS)
	}
	for i, seq := range []uint64{10, 11, 12, 13} {
		if p.Segments[i].MediaSequence != seq {
			t.Errorf("segment %d seq = %d, want %d", i, p.Segments[i].MediaSequence, seq)
		}
	}
}

func TestInvalidEXTINFOffsetPointsAtTag(t *testing.T) {
	data := []byte("#EXTM3U\n#EXTINF:abc,\nseg.m4s\n")
	p, err := Parse(data, "bad.m3u8")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	e := findByCode(t, p.Errors, model.CodeInvalidEXTINF)
	want := int64(bytes.Index(data, []byte("#EXTINF:abc")))
	if e.Offset != want {
		t.Fatalf("offset = %d, want %d", e.Offset, want)
	}
	if got := string(data[e.Offset : e.Offset+11]); got != "#EXTINF:abc" {
		t.Fatalf("bytes at offset = %q", got)
	}
}

func TestDuplicateAndUnknownMediaSequence(t *testing.T) {
	dup := "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:5\n#EXTINF:1,\na.m4s\n#EXT-X-MEDIA-SEQUENCE:5\n#EXTINF:1,\nb.m4s\n"
	p, err := Parse([]byte(dup), "dup.m3u8")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(p.Segments) != 2 {
		t.Fatalf("segments = %d, want 2 (no silent reordering)", len(p.Segments))
	}
	e := findByCode(t, p.Errors, model.CodeDuplicateMediaSeq)
	want := int64(bytes.LastIndex([]byte(dup), []byte("#EXT-X-MEDIA-SEQUENCE:5")))
	if e.Offset != want {
		t.Fatalf("duplicate offset = %d, want %d", e.Offset, want)
	}
	if p.Segments[1].MediaSequence != 6 {
		t.Fatalf("second segment sequence = %d, want 6", p.Segments[1].MediaSequence)
	}

	jump := "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:5\n#EXTINF:1,\na.m4s\n#EXT-X-MEDIA-SEQUENCE:9\n#EXTINF:1,\nb.m4s\n"
	p, err = Parse([]byte(jump), "jump.m3u8")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	findByCode(t, p.Errors, model.CodeUnknownMediaSeq)
	if len(p.Segments) != 2 || p.Segments[1].MediaSequence != 9 {
		t.Fatalf("segments reordered: %+v", p.Segments)
	}
}

func TestTruncatedPlaylist(t *testing.T) {
	data := []byte("#EXTM3U\n#EXTINF:1,\na.m4s\n#EXTINF:1,\n")
	p, err := Parse(data, "trunc.m3u8")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	e := findByCode(t, p.Errors, model.CodeHLSTruncated)
	want := int64(bytes.LastIndex(data, []byte("#EXTINF:1,")))
	if e.Offset != want {
		t.Fatalf("truncation offset = %d, want %d", e.Offset, want)
	}
	if len(p.Segments) != 1 {
		t.Fatalf("segments = %d, want 1", len(p.Segments))
	}

	cut := "#EXTM3U\n#EXTINF:1,\na.m4s\n#EXT-X-DISC"
	p, _ = Parse([]byte(cut), "cut.m3u8")
	e = findByCode(t, p.Errors, model.CodeHLSTruncated)
	if got := cut[e.Offset:]; got != "#EXT-X-DISC" {
		t.Fatalf("truncated tag offset points at %q", got)
	}
}

func TestMissingHeader(t *testing.T) {
	_, err := Parse([]byte("#EXTINF:1,\na.m4s\n"), "nohdr.m3u8")
	if err == nil {
		t.Fatal("Parse accepted playlist without #EXTM3U")
	}
}

func findByCode(t *testing.T, errs []*model.Error, code model.Code) *model.Error {
	t.Helper()
	for _, e := range errs {
		if e.Code == code {
			return e
		}
	}
	t.Fatalf("missing %s diagnostic in %v", code, errs)
	return nil
}
