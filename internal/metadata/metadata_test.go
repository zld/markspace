package metadata

import (
	"bytes"
	"strings"
	"testing"

	"replaypipe/internal/timebase"
)

const srtSample = "1\r\n00:00:02,000 --> 00:00:04,000\r\nkickoff\r\n\r\n" +
	"2\r\n00:00:06,000 --> 00:00:08,000\r\ncounter attack\r\n\r\n"

func TestParseSRT(t *testing.T) {
	events, err := ParseSRT(strings.NewReader(srtSample), ParseOptions{MediaEndNS: 10_000_000_000})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events", len(events))
	}
	if events[0].StartNS != 2_000_000_000 || events[0].EndNS != 4_000_000_000 {
		t.Fatalf("cue 0 timing = %d..%d", events[0].StartNS, events[0].EndNS)
	}
	if events[1].Text != "counter attack" {
		t.Fatalf("cue 1 text = %q", events[1].Text)
	}
	if events[0].ID == "" || events[0].DigestHex() == "" {
		t.Fatal("event lacks stable id/digest")
	}
}

func TestParseSRTRegression(t *testing.T) {
	doc := "1\r\n00:00:06,000 --> 00:00:08,000\r\nlate\r\n\r\n" +
		"2\r\n00:00:02,000 --> 00:00:04,000\r\nearly\r\n\r\n"
	_, err := ParseSRT(strings.NewReader(doc), ParseOptions{MediaEndNS: 10_000_000_000})
	if err == nil || err.Code != timebase.CodeTimeRegression {
		t.Fatalf("want TIME_REGRESSION, got %v", err)
	}
}

func TestParseSRTOutOfRange(t *testing.T) {
	doc := "1\r\n00:00:45,000 --> 00:00:46,000\r\nlate\r\n\r\n"
	_, err := ParseSRT(strings.NewReader(doc), ParseOptions{MediaEndNS: 10_000_000_000})
	if err == nil || err.Code != timebase.CodeEventOutOfRange {
		t.Fatalf("want EVENT_OUT_OF_RANGE, got %v", err)
	}
	if err.Offset < 0 {
		t.Fatalf("error lacks byte offset: %v", err)
	}
}

func TestParseSRTCorruptTiming(t *testing.T) {
	doc := "1\r\n00:00:xx,000 --> 00:00:04,000\r\nbad\r\n\r\n"
	_, err := ParseSRT(strings.NewReader(doc), ParseOptions{MediaEndNS: 10_000_000_000})
	if err == nil || err.Code != timebase.CodeCorruptField {
		t.Fatalf("want CORRUPT_FIELD, got %v", err)
	}
}

func TestParseWebVTT(t *testing.T) {
	doc := "WEBVTT\n\nNOTE a comment\nignored line\n\n" +
		"cue-1\n00:00:02.000 --> 00:00:04.000 align:start\nkickoff\n\n" +
		"00:06.000 --> 00:00:08.000\nshort form\n"
	events, err := ParseWebVTT(bytes.NewReader([]byte(doc)), ParseOptions{MediaEndNS: 10_000_000_000})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events", len(events))
	}
	if events[0].ID != "webvtt-0001-cue-1" {
		t.Fatalf("id = %q", events[0].ID)
	}
	if events[1].StartNS != 6_000_000_000 {
		t.Fatalf("short-form start = %d", events[1].StartNS)
	}
}

func TestParseWebVTTRejectsNonVTT(t *testing.T) {
	_, err := ParseWebVTT(strings.NewReader("not a vtt"), ParseOptions{})
	if err == nil || err.Code != timebase.CodeUnsupportedFormat {
		t.Fatalf("want UNSUPPORTED_FORMAT, got %v", err)
	}
}

func TestParseChapters(t *testing.T) {
	doc := `{"version":1,"timebase":"1000/1","chapters":[
		{"title":"kickoff","start":"00:00:02,000","end":"00:00:04,000"},
		{"title":"counter","start":6000,"end":8000}]}`
	events, err := ParseChapters(strings.NewReader(doc), ParseOptions{MediaEndNS: 10_000_000_000})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d chapters", len(events))
	}
	if events[0].StartNS != 2_000_000_000 {
		t.Fatalf("clock start = %d", events[0].StartNS)
	}
	if events[1].StartNS != 6_000_000_000 {
		t.Fatalf("integer start = %d", events[1].StartNS)
	}
	if events[0].Kind != KindChapter {
		t.Fatalf("kind = %s", events[0].Kind)
	}
}

func TestParseChaptersImplicitEndAndErrors(t *testing.T) {
	doc := `{"version":1,"chapters":[
		{"title":"a","start":0},
		{"title":"b","start":1000,"end":2000}]}`
	events, err := ParseChapters(strings.NewReader(doc), ParseOptions{MediaEndNS: 10_000_000_000})
	if err != nil {
		t.Fatal(err)
	}
	if events[0].EndNS != 1_000_000_000 {
		t.Fatalf("implicit end = %d", events[0].EndNS)
	}
	_, err = ParseChapters(strings.NewReader(`{"version":1,"chapters":[{"title":"a","start":0}]}`), ParseOptions{})
	if err == nil || err.Code != timebase.CodeCorruptField {
		t.Fatalf("want CORRUPT_FIELD for missing final end, got %v", err)
	}
	_, err = ParseChapters(strings.NewReader(`{"version":9,"chapters":[]}`), ParseOptions{})
	if err == nil || err.Code != timebase.CodeUnsupportedFormat {
		t.Fatalf("want UNSUPPORTED_FORMAT, got %v", err)
	}
}

func TestParseID3RoundTrip(t *testing.T) {
	tag := buildTestID3(t)
	events, err := ParseID3(tag, ParseOptions{MediaEndNS: 10_000_000_000, TrackID: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events", len(events))
	}
	if events[0].StartNS != 2_000_000_000 {
		t.Fatalf("first timestamp = %d", events[0].StartNS)
	}
	if events[1].Text != "event2" {
		t.Fatalf("second text = %q", events[1].Text)
	}
}

func TestParseID3Truncated(t *testing.T) {
	tag := buildTestID3(t)
	_, err := ParseID3(tag[:len(tag)-3], ParseOptions{MediaEndNS: 10_000_000_000})
	if err == nil || err.Code != timebase.CodeBoxTruncated {
		t.Fatalf("want BOX_TRUNCATED, got %v", err)
	}
}

func TestSEIRoundTrip(t *testing.T) {
	var stream []byte
	stream = append(stream, EncodeSEI(2_000_000_000, "kickoff")...)
	stream = append(stream, 0x00, 0x00, 0x00, 0x01, 0x65, 0x01, 0x02)
	stream = append(stream, EncodeSEI(6_000_000_000, "counter")...)
	events, err := ParseSEI(stream, ParseOptions{MediaEndNS: 10_000_000_000, TrackID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events", len(events))
	}
	if events[0].Text != "kickoff" || events[1].StartNS != 6_000_000_000 {
		t.Fatalf("unexpected events: %+v", events)
	}
}

func TestSplitAnnexBTruncated(t *testing.T) {
	_, err := SplitAnnexB([]byte{0, 0, 0, 1}, 0)
	if err == nil || err.Code != timebase.CodeBoxTruncated {
		t.Fatalf("want BOX_TRUNCATED, got %v", err)
	}
	nals, err := SplitAnnexB([]byte{0, 0, 0, 1, 0x65, 1, 2, 3, 0, 0, 1, 0x06, 9}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(nals) != 2 || nals[1].Type != 6 {
		t.Fatalf("nals = %+v", nals)
	}
}

func TestParseID3SyncsafeRejectsHighBits(t *testing.T) {
	tag := buildTestID3(t)
	tag[6] = 0x80
	_, err := ParseID3(tag, ParseOptions{MediaEndNS: 10_000_000_000})
	if err == nil || err.Code != timebase.CodeCorruptField {
		t.Fatalf("want CORRUPT_FIELD, got %v", err)
	}
}

// buildTestID3 constructs a two-event ID3v2.4 tag.
func buildTestID3(t *testing.T) []byte {
	t.Helper()
	frame := func(id string, payload []byte) []byte {
		sz := len(payload)
		h := make([]byte, 10)
		copy(h, id)
		h[4] = byte((sz >> 21) & 0x7f)
		h[5] = byte((sz >> 14) & 0x7f)
		h[6] = byte((sz >> 7) & 0x7f)
		h[7] = byte(sz & 0x7f)
		return append(h, payload...)
	}
	text := func(s string) []byte { return append([]byte{0x03}, []byte(s)...) }
	priv := func(ticks uint64) []byte {
		p := append([]byte{0x00}, []byte(tsPrivOwner)...)
		p = append(p, 0x00)
		var b [8]byte
		for i := 0; i < 8; i++ {
			b[i] = byte(ticks >> uint(56-8*i))
		}
		return append(p, b[:]...)
	}
	var body []byte
	body = append(body, frame("TXXX", append(append([]byte{0x03}, []byte("event")...), 0x00, 'k'))...)
	body = append(body, frame("PRIV", priv(180000))...)
	body = append(body, frame("TXXX", append(append([]byte{0x03}, []byte("event")...), 0x00, 's'))...)
	body = append(body, frame("TXXX", append(append([]byte{0x03}, []byte("event2")...), 0x00))...)
	body = append(body, frame("PRIV", priv(540000))...)
	body = append(body, frame("TIT2", text("second"))...)
	return id3TagForTest(body)
}

func id3TagForTest(body []byte) []byte {
	n := len(body)
	hdr := []byte{'I', 'D', '3', 0x04, 0x00, 0x00,
		byte((n >> 21) & 0x7f), byte((n >> 14) & 0x7f), byte((n >> 7) & 0x7f), byte(n & 0x7f)}
	return append(hdr, body...)
}
