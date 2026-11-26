package scan

import (
	"encoding/binary"
	"strings"
	"testing"
)

func TestScanWebVTT(t *testing.T) {
	vtt := strings.Join([]string{
		"WEBVTT",
		"",
		"00:00:01.000 --> 00:00:02.500",
		"first cue",
		"",
		"00:00:03.250 --> 00:00:04.000 align:start",
		"second",
		"",
	}, "\n")
	res, err := ScanWebVTT(strings.NewReader(vtt), "a.vtt", "h1", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Events) != 2 {
		t.Fatalf("events = %d", len(res.Events))
	}
	c0 := res.Events[0]
	if c0.Tick != 1000 || c0.DurTick != 1500 || c0.TrackID != 5 {
		t.Fatalf("cue0 wrong: %+v", c0)
	}
	if c0.TimeBase.Den != 1000 {
		t.Fatalf("vtt time base = %+v", c0.TimeBase)
	}
	// Second cue has trailing settings and a fractional start.
	c1 := res.Events[1]
	if c1.Tick != 3250 || c1.Offset <= c0.Offset {
		t.Fatalf("cue1 wrong: %+v", c1)
	}
}

func TestScanWebVTTBadTimingKeepsGoing(t *testing.T) {
	vtt := "WEBVTT\n\n00:00:01.000 --> not-a-clock\n\n00:00:02.000 --> 00:00:03.000\nok\n"
	res, err := ScanWebVTT(strings.NewReader(vtt), "b.vtt", "h", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Anomalies) != 1 || res.Anomalies[0].Type != "UNPARSEABLE" {
		t.Fatalf("expected one unparseable anomaly, got %+v", res.Anomalies)
	}
	if len(res.Events) != 1 || res.Events[0].Tick != 2000 {
		t.Fatalf("good cue must survive: %+v", res.Events)
	}
}

func TestScanWebVTTMissingHeader(t *testing.T) {
	if _, err := ScanWebVTT(strings.NewReader("nope\n"), "c.vtt", "h", 1); err == nil {
		t.Fatal("non-WebVTT input must error")
	}
}

func TestScanChapters(t *testing.T) {
	ch := strings.Join([]string{
		"CHAPTER01=00:00:00.000",
		"CHAPTER01NAME=Intro",
		"CHAPTER02=00:01:30.500",
		"CHAPTER02NAME=Main",
		"",
	}, "\n")
	res, err := ScanChapters(strings.NewReader(ch), "ch.txt", "h", 9)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Events) != 2 {
		t.Fatalf("chapter events = %d", len(res.Events))
	}
	if res.Events[0].Tick != 0 || string(res.Events[0].Payload) != "Intro" {
		t.Fatalf("chapter0 wrong: %+v", res.Events[0])
	}
	if res.Events[1].Tick != 90_500 || res.Events[1].ID != "CHAPTER02" {
		t.Fatalf("chapter1 wrong: %+v", res.Events[1])
	}
}

func TestScanID3(t *testing.T) {
	var tag []byte
	tag = append(tag, []byte("ID3")...)
	tag = append(tag, 0x04, 0x00, 0x00) // 2.4, no flags
	// Two frames: TXXX with 8-byte body (4-byte tick + data), WXXX with 12.
	frame := func(id string, body []byte) []byte {
		f := []byte(id)
		size := make([]byte, 4)
		binary.BigEndian.PutUint32(size, uint32(len(body)))
		f = append(f, size...)
		f = append(f, 0, 0) // frame flags
		return append(f, body...)
	}
	b1 := append(u32(2500), []byte("abcd")...) // tick 2500ms
	b2 := append(u32(5000), make([]byte, 8)...)
	frames := append(frame("TXXX", b1), frame("WXXX", b2)...)
	ss := make([]byte, 4) // syncsafe size
	n := len(frames)
	ss[0], ss[1], ss[2], ss[3] = byte(n>>21), byte(n>>14), byte(n>>7), byte(n)
	tag = append(tag, ss...)
	tag = append(tag, frames...)

	res, err := ScanID3(strings.NewReader(string(tag)), "m.id3", "h", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Events) != 2 {
		t.Fatalf("id3 events = %d", len(res.Events))
	}
	if res.Events[0].ID != "TXXX" || res.Events[0].Tick != 2500 {
		t.Fatalf("frame0 wrong: %+v", res.Events[0])
	}
	if res.Events[1].Tick != 5000 || res.Events[1].Offset != int64(len(tag)-len(frame("WXXX", b2))) {
		t.Fatalf("frame1 wrong: %+v", res.Events[1])
	}
}

func TestScanID3Truncated(t *testing.T) {
	// Declared size exceeds the actual bytes: anomaly, not a hard error.
	tag := []byte("ID3\x04\x00\x00")
	tag = append(tag, 0x02, 0, 0, 0) // syncsafe => 2 MiB
	tag = append(tag, 0, 0)
	res, err := ScanID3(strings.NewReader(string(tag)), "t.id3", "h", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Anomalies) != 1 {
		t.Fatalf("expected truncation anomaly, got %+v", res.Anomalies)
	}
}
