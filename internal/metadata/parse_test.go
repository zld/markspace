package metadata

import (
	"bytes"
	"testing"

	"archive.example/mediakernel/internal/events"

	"github.com/abema/go-mp4"
)

// mp4writer accumulates boxes into a document.
type mp4writer struct {
	buf bytes.Buffer
}

func (w *mp4writer) add(typ string, payload []byte) {
	w.buf.Write(wrapBox(typ, payload))
}

// buildTestDoc writes a fragmented MP4 with declared tracks at the given
// timescales and one moof with the supplied timed boxes per track ID.
func buildTestDoc(t *testing.T, subScale, chapScale uint32, subBoxes, chapBoxes []byte) []byte {
	t.Helper()
	var w mp4writer
	w.add("ftyp", append([]byte{'i', 's', 'o', '5', 0, 0, 0, 0}, []byte("arch")...))

	var moov []byte
	mvhd := &mp4.Mvhd{
		FullBox:     mp4.FullBox{Version: 0},
		Timescale:   1000,
		DurationV0:  60000,
		Rate:        0x00010000,
		Volume:      0x0100,
		Matrix:      [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000},
		NextTrackID: 7,
	}
	var mvbuf bytes.Buffer
	if _, err := mp4.Marshal(&mvbuf, mvhd, mp4.Context{}); err != nil {
		t.Fatal(err)
	}
	moov = append(moov, wrapBox("mvhd", mvbuf.Bytes())...)
	moov = append(moov, trakSpec(t, "subt", "subtitle", 3, subScale)...)
	moov = append(moov, trakSpec(t, "text", "chapter", 4, chapScale)...)
	w.add("moov", moov)

	// One moof per track. The chapter fragment precedes the subtitle one so
	// cutting the subtitle tail leaves the chapter completely intact.
	if len(chapBoxes) > 0 {
		w.add("moof", append(wrapBox("mfhd", append(fullBox(0, 0), 0, 0, 0, 2)),
			wrapBox("traf", append(tfhdBytes(4), chapBoxes...))...))
	}
	if len(subBoxes) > 0 {
		w.add("moof", append(wrapBox("mfhd", append(fullBox(0, 0), 0, 0, 0, 1)),
			wrapBox("traf", append(tfhdBytes(3), subBoxes...))...))
	}
	w.add("mfra", nil)
	return w.buf.Bytes()
}

func trakSpec(t *testing.T, handler, name string, trackID, scale uint32) []byte {
	t.Helper()
	var body []byte
	tkhd := &mp4.Tkhd{
		FullBox:    mp4.FullBox{Version: 0},
		TrackID:    trackID,
		DurationV0: 60000,
		Matrix:     [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000},
	}
	var tkbuf bytes.Buffer
	if _, err := mp4.Marshal(&tkbuf, tkhd, mp4.Context{}); err != nil {
		t.Fatal(err)
	}
	body = append(body, wrapBox("tkhd", tkbuf.Bytes())...)

	var mdia []byte
	mdhd := &mp4.Mdhd{
		FullBox:    mp4.FullBox{Version: 0},
		Timescale:  scale,
		DurationV0: 60000,
		Language:   [3]byte{byte('u' - 0x60), byte('n' - 0x60), byte('d' - 0x60)},
	}
	var mdhdBuf bytes.Buffer
	if _, err := mp4.Marshal(&mdhdBuf, mdhd, mp4.Context{}); err != nil {
		t.Fatal(err)
	}
	mdia = append(mdia, wrapBox("mdhd", mdhdBuf.Bytes())...)
	hdlr := &mp4.Hdlr{
		FullBox:     mp4.FullBox{Version: 0},
		HandlerType: [4]byte{handler[0], handler[1], handler[2], handler[3]},
		Name:        name,
	}
	var hbuf bytes.Buffer
	if _, err := mp4.Marshal(&hbuf, hdlr, mp4.Context{}); err != nil {
		t.Fatal(err)
	}
	mdia = append(mdia, wrapBox("hdlr", hbuf.Bytes())...)
	body = append(body, wrapBox("mdia", mdia)...)
	return wrapBox("trak", body)
}

func tfhdBytes(trackID uint32) []byte {
	return wrapBox("tfhd", append(fullBox(0, 0x020000), uint32be(trackID)...))
}

func wrapBox(typ string, payload []byte) []byte {
	out := make([]byte, 8+len(payload))
	binaryPutU32(out[0:4], uint32(len(out)))
	copy(out[4:8], typ)
	copy(out[8:], payload)
	return out
}

func fullBox(version uint8, flags uint32) []byte {
	return []byte{version, byte(flags >> 16), byte(flags >> 8), byte(flags)}
}

func uint32be(v uint32) []byte {
	b := make([]byte, 4)
	binaryPutU32(b, v)
	return b
}

func binaryPutU32(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v>>24), byte(v>>16), byte(v>>8), byte(v)
}

func binaryPutU64(b []byte, v uint64) {
	b[0], b[1], b[2], b[3] = byte(v>>56), byte(v>>48), byte(v>>40), byte(v>>32)
	b[4], b[5], b[6], b[7] = byte(v>>24), byte(v>>16), byte(v>>8), byte(v)
}

func makeTimedBox(kind string, presentation uint64, duration uint32, seq uint16, text string, payload []byte) []byte {
	p := make([]byte, TimedBoxHeaderLen)
	p[0] = 1
	binaryPutU64(p[4:12], presentation)
	binaryPutU32(p[12:16], duration)
	p[16] = byte(seq >> 8)
	p[17] = byte(seq)
	p = append(p, []byte(text)...)
	p = append(p, 0)
	p = append(p, payload...)
	return wrapBox(kind, p)
}

func TestParseDistinctTimescalesMapToSameInstant(t *testing.T) {
	// 1.5s at 1000 ticks (subtitle) and 750 ticks at 500 (chapter) both land
	// on 1.5s only when each uses its own mdhd scale.
	sub := append(makeTimedBox(BoxSubtitle, 1500, 200, 1, "sub", nil),
		makeTimedBox(BoxSubtitle, 2500, 200, 2, "sub2", nil)...)
	chap := makeTimedBox(BoxChapter, 750, 500, 1, "chap", nil)
	doc, err := ParseBytes(buildTestDoc(t, 1000, 500, sub, chap))
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Tracks) != 2 {
		t.Fatalf("tracks %d", len(doc.Tracks))
	}
	var gotSub, gotChap int64 = -1, -2
	for _, e := range doc.Events {
		switch e.Track {
		case events.TrackSubtitle:
			if e.Seq == 1 {
				gotSub = e.At
			}
		case events.TrackChapter:
			gotChap = e.At
		}
	}
	if gotSub != gotChap {
		t.Fatalf("subtitle@%dns chapter@%dns should coincide", gotSub, gotChap)
	}
	if gotSub != 1_500_000_000 {
		t.Fatalf("subtitle at %d", gotSub)
	}
}

func TestParseTimescaleZeroIsFatal(t *testing.T) {
	doc, err := ParseBytes(buildTestDoc(t, 0, 1000,
		makeTimedBox(BoxSubtitle, 100, 10, 1, "x", nil), nil))
	if err == nil {
		t.Fatal("expected fatal error")
	}
	ae, ok := err.(*events.Error)
	if !ok || ae.Code != events.CodeInvalidTimescale {
		t.Fatalf("got %v", err)
	}
	if doc.FatalErr() == nil {
		t.Fatal("document should expose a fatal error")
	}
}

func TestParseTruncationReportsTrackAndOffset(t *testing.T) {
	sub := append(makeTimedBox(BoxSubtitle, 100, 50, 1, "first", nil),
		makeTimedBox(BoxSubtitle, 200, 50, 2, "second-with-a-long-text", nil)...)
	full := buildTestDoc(t, 1000, 1000, sub, makeTimedBox(BoxChapter, 100, 100, 1, "chapter", nil))
	second := bytes.LastIndex(full, []byte(BoxSubtitle))
	if second < 0 {
		t.Fatal("no second subtitle box")
	}
	truncated := full[:second+8]

	doc, err := ParseBytes(truncated)
	if err == nil {
		t.Fatal("expected recoverable truncation error")
	}
	ae, ok := err.(*events.Error)
	if !ok || ae.Code != events.CodeTruncated {
		t.Fatalf("got %v", err)
	}
	if ae.Offset <= 0 {
		t.Fatalf("offset %d", ae.Offset)
	}
	if ae.Track != events.TrackSubtitle {
		t.Fatalf("expected subtitle track attribution, got %q", ae.Track)
	}
	var subs, chaps int
	for _, e := range doc.Events {
		switch e.Track {
		case events.TrackSubtitle:
			subs++
		case events.TrackChapter:
			chaps++
		}
	}
	if subs != 1 {
		t.Fatalf("subtitle events %d, want 1 complete", subs)
	}
	if chaps != 1 {
		t.Fatalf("chapter events %d, want 1", chaps)
	}
}

func TestParseBoundaryTimestampOutOfRange(t *testing.T) {
	_, err := ParseBytes(buildTestDoc(t, 1000, 1000,
		makeTimedBox(BoxSubtitle, 0xFFFF_FFFF, 1, 1, "huge", nil), nil))
	if err == nil {
		t.Fatal("expected out-of-range error")
	}
	if ae, ok := err.(*events.Error); !ok || ae.Code != events.CodeTimeOutOfRange {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseZeroSequenceIsPreserved(t *testing.T) {
	doc, err := ParseBytes(buildTestDoc(t, 1000, 1000,
		makeTimedBox(BoxSubtitle, 0, 10, 0, "zero", nil), nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Events) != 1 {
		t.Fatalf("events %d", len(doc.Events))
	}
	if doc.Events[0].At != 0 || doc.Events[0].Seq != 0 {
		t.Fatalf("zero baseline lost: %+v", doc.Events[0])
	}
}

func TestDecodeID3(t *testing.T) {
	payload := testID3("hello")
	text, rest, ok := DecodeID3(payload)
	if !ok || text != "hello" || len(rest) == 0 {
		t.Fatalf("decode: ok=%v text=%q rest=%d", ok, text, len(rest))
	}
	if _, _, ok := DecodeID3([]byte("not-id3")); ok {
		t.Fatal("garbage accepted")
	}
	if _, _, ok := DecodeID3(payload[:8]); ok {
		t.Fatal("short payload accepted")
	}
}

func TestDecodeSEI(t *testing.T) {
	payload := testSEI("msg")
	text, rest, ok := DecodeSEI(payload)
	if !ok {
		t.Fatal("decode failed")
	}
	if want := "31363032-3800-0000-6172-636869766531:msg"; text != want {
		t.Fatalf("text %q want %q", text, want)
	}
	if len(rest) != 2 {
		t.Fatalf("rest %d", len(rest))
	}
	if _, _, ok := DecodeSEI([]byte{0x1f, 0, 0}); ok {
		t.Fatal("non-SEI accepted")
	}
}

func TestParseDeterministic(t *testing.T) {
	sub := append(makeTimedBox(BoxSubtitle, 100, 10, 1, "a", []byte{1}),
		makeTimedBox(BoxSubtitle, 200, 10, 2, "b", []byte{2})...)
	raw := buildTestDoc(t, 1000, 500, sub, makeTimedBox(BoxChapter, 100, 10, 1, "c", nil))
	one, err := ParseBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	two, err := ParseBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(one.Events) != len(two.Events) {
		t.Fatal("event count differs")
	}
	for i := range one.Events {
		if events.CompareEvents(one.Events[i], two.Events[i]) != 0 {
			t.Fatalf("event %d differs", i)
		}
	}
}

func testID3(text string) []byte {
	body := append([]byte{3}, []byte(text)...)
	frame := append([]byte{'T', 'I', 'T', '2'}, uint32be(uint32(len(body)))[0:]...)
	frame = append(frame, 0, 0)
	frame = append(frame, body...)
	header := []byte{'I', 'D', '3', 3, 0, 0, 0, 0, 0, 0}
	header[6] = byte((len(frame) >> 21) & 0x7f)
	header[7] = byte((len(frame) >> 14) & 0x7f)
	header[8] = byte((len(frame) >> 7) & 0x7f)
	header[9] = byte(len(frame) & 0x7f)
	return append(header, frame...)
}

func testSEI(msg string) []byte {
	uuid := []byte{0x31, 0x36, 0x30, 0x32, 0x38, 0x00, 0x00, 0x00,
		0x61, 0x72, 0x63, 0x68, 0x69, 0x76, 0x65, 0x31}
	body := append(uuid, []byte(msg)...)
	body = append(body, 0)
	out := []byte{0x06, 0x05, byte(len(body))}
	out = append(out, body...)
	out = append(out, 0xF0, 0x01)
	return out
}
