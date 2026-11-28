package media

import (
	"bytes"
	"testing"

	"github.com/abema/go-mp4"

	"mediad/fixture"
	"mediad/timeline"
)

func TestParseSubtitleSamples(t *testing.T) {
	base := timeline.Base{Name: "sub", TimeScale: 1000}
	mk := func(text string) []byte {
		if len(text) > 0xffff {
			panic("too long")
		}
		out := []byte{byte(len(text) >> 8), byte(len(text))}
		return append(out, []byte(text)...)
	}
	samples := [][]byte{mk("Hello"), mk("World"), {0x00, 0x05, 'x'}} // third truncated
	events, issues := ParseSubtitleSamples(samples, 3, "sub.mp4", base,
		[]int64{0, 2000, 4000}, []timeline.Rational{timeline.MustRational(2, 1), timeline.MustRational(2, 1), timeline.MustRational(1, 1)})
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2 (truncated sample excluded)", len(events))
	}
	if string(events[0].Payload) != "Hello" {
		t.Fatalf("first cue = %q", events[0].Payload)
	}
	if ms, _ := events[1].Presentation.Micros(); ms != 2_000_000 {
		t.Fatalf("second cue at %d µs", ms)
	}
	if !issues.HasErrors() || len(issues) != 1 {
		t.Fatalf("issues = %v", issues)
	}
	if issues[0].Code != timeline.CodeInvalidLength || issues[0].FrameIndex != 2 {
		t.Fatalf("issue = %+v", issues[0])
	}
}

func TestParseSubtitleEmptySampleSkippedSilently(t *testing.T) {
	base := timeline.Base{Name: "sub", TimeScale: 1000}
	events, issues := ParseSubtitleSamples([][]byte{{0, 0}}, 3, "s.mp4", base, []int64{0}, nil)
	if len(events) != 0 || issues.HasErrors() {
		t.Fatalf("empty cue should produce nothing and no error: %d %v", len(events), issues)
	}
}

func TestParseChpl(t *testing.T) {
	// reserved(4) + count + [start100ns(8), titleLen(1), title]
	payload := []byte{0, 0, 0, 0, 2}
	payload = append(payload, fixture.U64(0)...)
	payload = append(payload, 5)
	payload = append(payload, []byte("Intro")...)
	payload = append(payload, fixture.U64(150_000_000)...) // 15 s
	payload = append(payload, 4)
	payload = append(payload, []byte("Main")...)
	events, issues := ParseChpl(payload, "chapters.mp4", 0)
	if issues.HasErrors() {
		t.Fatal(issues)
	}
	if len(events) != 2 {
		t.Fatalf("chapters = %d", len(events))
	}
	if string(events[0].Payload) != "Intro" || events[0].Kind != timeline.KindChapter {
		t.Fatalf("chapter 0 = %+v", events[0])
	}
	if ms, _ := events[1].Presentation.Micros(); ms != 15_000_000 {
		t.Fatalf("chapter 1 at %d µs, want 15 s", ms)
	}
}

func TestParseChplTruncated(t *testing.T) {
	payload := []byte{0, 0, 0, 0, 1, 0, 0, 0} // claims one chapter but too short
	_, issues := ParseChpl(payload, "ch.mp4", 0)
	if !issues.HasErrors() {
		t.Fatal("truncated chpl must be reported")
	}
}

func TestParseEmsgV1AndV0(t *testing.T) {
	// Version 1 layout: version/flags, timescale, presentation_time,
	// event_duration, id, scheme\0, value\0, message.
	v1 := make([]byte, 0)
	v1 = append(v1, 1, 0, 0, 0)
	v1 = append(v1, fixture.U32(1000)...)
	v1 = append(v1, fixture.U64(4500)...) // 4.5 s
	v1 = append(v1, fixture.U32(500)...)
	v1 = append(v1, fixture.U32(7)...)
	v1 = append(v1, []byte("urn:test")...)
	v1 = append(v1, 0)
	v1 = append(v1, 0) // empty value
	v1 = append(v1, []byte("payload")...)
	info, err := ParseEmsg(v1, "a.mp4", 128)
	if err != nil {
		t.Fatal(err)
	}
	if info.Scheme != "urn:test" || info.Timescale != 1000 || info.PresentationTime != 4500 || string(info.Message) != "payload" {
		t.Fatalf("emsg v1 = %+v", info)
	}
	ev, err := EmsgToEvent(info, "a.mp4", 0)
	if err != nil {
		t.Fatal(err)
	}
	if ms, _ := ev.Presentation.Micros(); ms != 4_500_000 {
		t.Fatalf("emsg v1 time %d µs", ms)
	}
	if ev.Source.ByteOffset != 128 || ev.Source.Box != "emsg" {
		t.Fatalf("emsg source = %+v", ev.Source)
	}

	// Version 0 layout: version/flags, scheme\0, value\0, timescale,
	// presentation_time_delta, event_duration, id, message.
	v0 := []byte{0, 0, 0, 0}
	v0 = append(v0, []byte("urn:v0")...)
	v0 = append(v0, 0)
	v0 = append(v0, 0) // empty value
	v0 = append(v0, fixture.U32(90000)...)
	v0 = append(v0, fixture.U32(9000)...) // 100 ms delta
	v0 = append(v0, fixture.U32(0)...)
	v0 = append(v0, fixture.U32(1)...)
	v0 = append(v0, []byte("d")...)
	info0, err := ParseEmsg(v0, "a.mp4", 256)
	if err != nil {
		t.Fatal(err)
	}
	if info0.Scheme != "urn:v0" || info0.DeltaTime != 9000 {
		t.Fatalf("emsg v0 = %+v", info0)
	}
	ev0, err := EmsgToEvent(info0, "a.mp4", 180000) // 2 s segment base
	if err != nil {
		t.Fatal(err)
	}
	if ms, _ := ev0.Presentation.Micros(); ms != 2_100_000 {
		t.Fatalf("emsg v0 time %d µs", ms)
	}
}

func TestParseEmsgTruncated(t *testing.T) {
	// Declares a long scheme string with a short payload.
	bad := []byte{1, 0, 0, 0}
	bad = append(bad, bytes.Repeat([]byte{'x'}, 200)...) // no NUL terminator, short box
	_, err := ParseEmsg(bad, "bad.mp4", 64)
	if err == nil {
		t.Fatal("truncated emsg must be rejected")
	}
	issue, ok := timeline.IssueOf(err)
	if !ok || issue.ByteOffset != 64 {
		t.Fatalf("issue = %+v ok=%v", issue, ok)
	}
	// The same box type that go-mp4 itself refuses still deserves an offset.
	if issue.File != "bad.mp4" {
		t.Fatalf("file = %q", issue.File)
	}
}

func TestParseEmsgZeroTimescale(t *testing.T) {
	payload := []byte{0, 0, 0, 0}
	payload = append(payload, 0, 0) // empty scheme, empty value
	payload = append(payload, fixture.U32(0)...)
	payload = append(payload, fixture.U32(0)...)
	payload = append(payload, fixture.U32(0)...)
	payload = append(payload, fixture.U32(1)...)
	_, err := ParseEmsg(payload, "z.mp4", 0)
	if timeline.CodeOf(err) != timeline.CodeInvalidTime {
		t.Fatalf("code = %q", timeline.CodeOf(err))
	}
}

func TestParseID3TruncatedAndValid(t *testing.T) {
	// Build a valid v2.3 tag with one TIT2 frame and one TXXX frame.
	var body []byte
	body = append(body, "TIT2"...)
	body = append(body, fixture.U32(5)...)
	body = append(body, 0, 0)
	body = append(body, []byte("Title")...)
	body = append(body, "TXXX"...)
	body = append(body, fixture.U32(4)...)
	body = append(body, 0, 0)
	body = append(body, []byte("Data")...)
	tag := []byte{'I', 'D', '3', 3, 0, 0}
	sz := len(body)
	tag = append(tag, byte(sz>>21&0x7f), byte(sz>>14&0x7f), byte(sz>>7&0x7f), byte(sz&0x7f))
	tag = append(tag, body...)
	frames, err := ParseID3(tag, "id3.mp3", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 2 || frames[0].ID != "TIT2" || string(frames[1].Data) != "Data" {
		t.Fatalf("frames = %+v", frames)
	}

	// Truncate the declared tag size beyond the buffer.
	bad := append([]byte{}, tag...)
	bad[9] = bad[9] + 0x20
	if _, err := ParseID3(bad, "id3.mp3", 0); err == nil {
		t.Fatal("truncated ID3 tag must be rejected")
	}
}

func TestParseID3FrameOverrun(t *testing.T) {
	body := []byte("TIT2")
	body = append(body, fixture.U32(1000)...) // declares far more than present
	body = append(body, 0, 0)
	tag := []byte{'I', 'D', '3', 3, 0, 0}
	sz := len(body)
	tag = append(tag, byte(sz>>21&0x7f), byte(sz>>14&0x7f), byte(sz>>7&0x7f), byte(sz&0x7f))
	tag = append(tag, body...)
	_, err := ParseID3(tag, "i.mp3", 0)
	if timeline.CodeOf(err) != timeline.CodeInvalidLength {
		t.Fatalf("code = %q (err=%v)", timeline.CodeOf(err), err)
	}
}

func TestParseSEI(t *testing.T) {
	uuid := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	body := append([]byte{}, uuid...)
	body = append(body, []byte("meta")...)
	sei := []byte{0x06, 0x05, byte(len(body))}
	sei = append(sei, body...)
	sei = append(sei, 0x80) // rbsp trailing bits
	sample := append([]byte{0, 0, 0, 1}, sei...)
	msgs, issues := ParseSEI(sample, "v.mp4", 1, 100)
	if issues.HasErrors() {
		t.Fatal(issues)
	}
	if len(msgs) != 1 || len(msgs[0].UUID) != 16 || string(msgs[0].Body) != "meta" {
		t.Fatalf("msgs = %+v", msgs)
	}
	base := timeline.Base{Name: "v", TimeScale: 90000}
	events, _ := SEIToEvents(msgs, "v.mp4", 1, base, 90000)
	if len(events) != 1 {
		t.Fatalf("events = %d", len(events))
	}
	if ms, _ := events[0].Presentation.Micros(); ms != 1_000_000 {
		t.Fatalf("SEI event at %d µs", ms)
	}
	if events[0].Kind != timeline.KindSEI {
		t.Fatalf("kind = %s", events[0].Kind)
	}
}

func TestParseSEITruncatedPayload(t *testing.T) {
	// SEI NAL declaring a 40-byte payload but supplying 4 bytes.
	sample := []byte{0, 0, 0, 1, 0x06, 0x05, 40, 'a', 'b', 'c', 'd'}
	_, issues := ParseSEI(sample, "v.mp4", 1, 0)
	if !issues.HasErrors() {
		t.Fatal("truncated SEI payload must be reported")
	}
	if issues[0].ByteOffset == 0 {
		t.Fatal("issue should carry the byte offset of the NAL unit")
	}
}

func TestGoMp4UnmarshalEmsgRoundTrip(t *testing.T) {
	// Guard the assumption that go-mp4 parses v1 emsg with scheme strings.
	payload := []byte{1, 0, 0, 0}
	payload = append(payload, fixture.U32(1000)...)
	payload = append(payload, fixture.U64(2000)...)
	payload = append(payload, fixture.U32(100)...)
	payload = append(payload, fixture.U32(1)...)
	payload = append(payload, []byte("s")...)
	payload = append(payload, 0, 0) // empty value
	payload = append(payload, 'd')
	var box mp4.Emsg
	if _, err := mp4.Unmarshal(bytes.NewReader(payload), uint64(len(payload)), &box, mp4.Context{}); err != nil {
		t.Fatalf("go-mp4 emsg: %v", err)
	}
	if box.Timescale != 1000 || box.PresentationTime != 2000 || box.SchemeIdUri != "s" {
		t.Fatalf("box = %+v", box)
	}
}
