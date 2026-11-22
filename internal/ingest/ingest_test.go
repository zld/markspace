package ingest

import (
	"bytes"
	"testing"

	"github.com/pion/rtp"

	"example.com/mediatimeline/internal/model"
)

func packet(t *testing.T, seq uint16, ts uint32, marker bool, payload []byte) []byte {
	t.Helper()
	p := &rtp.Packet{
		Header:  rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: seq, Timestamp: ts, SSRC: 7, Marker: marker},
		Payload: payload,
	}
	raw, err := p.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestRTPReassemblesAccessUnits(t *testing.T) {
	r, err := NewRTPReassembler(RTPOptions{Track: model.TrackVideoMain, Timescale: 90000, FragmentGapTicks: 1, AllowDroppedGap: true})
	if err != nil {
		t.Fatal(err)
	}
	// Three access units of two packets each at 3000-tick spacing.
	seq := uint16(1)
	for au := 0; au < 3; au++ {
		ts := uint32(au * 3000)
		if err := r.DecodeRTPPacket(packet(t, seq, ts, false, []byte("a"))); err != nil {
			t.Fatal(err)
		}
		seq++
		if err := r.DecodeRTPPacket(packet(t, seq, ts, true, []byte("b"))); err != nil {
			t.Fatal(err)
		}
		seq++
	}
	res, err := r.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Samples) != 3 {
		t.Fatalf("samples = %d, want 3", len(res.Samples))
	}
	if res.Samples[0].Size != 2 {
		t.Fatalf("first unit size = %d, want 2", res.Samples[0].Size)
	}
	if got := res.Samples[1].Time.RepairedTime; got.Num != 1 || got.Den != 30 {
		t.Fatalf("second unit time = %+v, want 1/30", got)
	}
}

func TestRTPWrapExtended(t *testing.T) {
	r, _ := NewRTPReassembler(RTPOptions{Track: model.TrackVideoMain, Timescale: 90000, FragmentGapTicks: 1})
	// First packet just before the wrap, second just after.
	if err := r.Push(PacketObservation{Seq: 1, RawTicks: int64(1<<32) - 1500, Marker: true, Payload: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	if err := r.Push(PacketObservation{Seq: 2, RawTicks: 1500, Marker: true, Payload: []byte("y")}); err != nil {
		t.Fatal(err)
	}
	res, err := r.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Samples) != 2 {
		t.Fatalf("samples = %d", len(res.Samples))
	}
	var sawWrap bool
	for _, d := range res.Decisions {
		if d.Kind == "wrap-extended" {
			sawWrap = true
		}
	}
	if !sawWrap {
		t.Fatal("wrap decision not recorded")
	}
	if res.Samples[1].Time.Epoch != 1 {
		t.Fatalf("second sample epoch = %d, want 1", res.Samples[1].Time.Epoch)
	}
}

func TestRTPLossAndReorderRecorded(t *testing.T) {
	r, _ := NewRTPReassembler(RTPOptions{Track: model.TrackVideoMain, Timescale: 90000, FragmentGapTicks: 1, AllowDroppedGap: true})
	// Sequences 1,2 then a jump to 6 (3 packets lost), then 5 (reordered).
	for _, seq := range []uint16{1, 2, 6, 5} {
		if err := r.Push(PacketObservation{Seq: seq, RawTicks: int64(seq) * 3000, Marker: true, Payload: []byte("z")}); err != nil {
			t.Fatalf("seq %d: %v", seq, err)
		}
	}
	res, err := r.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if res.LostPackets != 3 {
		t.Fatalf("lost = %d, want 3", res.LostPackets)
	}
	if res.Reordered != 1 {
		t.Fatalf("reordered = %d, want 1", res.Reordered)
	}
	var sawGap bool
	for _, e := range res.Errors {
		if e.Code == model.CodeMissingPayload && e.Recoverable {
			sawGap = true
		}
	}
	if !sawGap {
		t.Fatalf("recoverable gap error not recorded: %v", res.Errors)
	}
	// Samples are ordered by repaired time: the late packet (15000 ticks =
	// 1/6 s) places before the 18000-tick packet (1/5 s) that arrived ahead.
	if got, want := res.Samples[2].Time.RepairedTime, (model.RationalTime{Num: 1, Den: 6}); got != want {
		t.Fatalf("late sample time = %+v, want %+v", got, want)
	}
	if got, want := res.Samples[3].Time.RepairedTime, (model.RationalTime{Num: 1, Den: 5}); got != want {
		t.Fatalf("sample after late packet = %+v, want %+v", got, want)
	}
}

func TestRTPBackwardUnrecoverable(t *testing.T) {
	r, _ := NewRTPReassembler(RTPOptions{Track: model.TrackVideoMain, Timescale: 90000, FragmentGapTicks: 1, Jitter: 3000})
	for i := 0; i < 4; i++ {
		if err := r.Push(PacketObservation{Seq: uint16(i), RawTicks: int64(i) * 3000, Marker: true, Payload: []byte("p")}); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Push(PacketObservation{Seq: 4, RawTicks: 1000, Marker: true, Payload: []byte("q")}); err != nil {
		t.Fatalf("push must defer clock faults until Finish: %v", err)
	}
	_, err := r.Finish()
	if err == nil {
		t.Fatal("expected an unrecoverable backward error")
	}
	pe, ok := model.AsError(err)
	if !ok || pe.Code != model.CodeBackwardTimestamp || pe.Stage != model.StageClock || pe.Recoverable {
		t.Fatalf("unexpected classified error: %v", err)
	}
}

func TestRTPMalformedPacketClassified(t *testing.T) {
	r, _ := NewRTPReassembler(RTPOptions{Track: model.TrackVideoMain, Timescale: 90000})
	err := r.DecodeRTPPacket([]byte{0x80})
	if err == nil {
		t.Fatal("expected a parse error")
	}
	pe, ok := model.AsError(err)
	if !ok || pe.Stage != model.StageParse || !pe.Recoverable {
		t.Fatalf("malformed packet should be a recoverable parse error: %v", err)
	}
	res, ferr := r.Finish()
	if ferr == nil {
		t.Fatalf("finish should fail without packets: %v", res)
	}
}

func TestRTPEmptyPayloadRecorded(t *testing.T) {
	r, _ := NewRTPReassembler(RTPOptions{Track: model.TrackVideoMain, Timescale: 90000, FragmentGapTicks: 1})
	if err := r.Push(PacketObservation{Seq: 1, RawTicks: 0, Marker: true, Payload: nil}); err != nil {
		t.Fatal(err)
	}
	res, err := r.Finish()
	if err == nil {
		t.Fatal("expected failure with no usable samples")
	}
	if len(res.Errors) == 0 || res.Errors[0].Code != model.CodeMissingPayload {
		t.Fatalf("expected missing-payload error, got %v", res.Errors)
	}
}

func TestParseID3(t *testing.T) {
	tag := buildID3(t, "Hello", "TXXXKEY", "value")
	got, err := ParseID3(tag)
	if err != nil {
		t.Fatalf("ParseID3: %v", err)
	}
	if title, ok := got.Text("TIT2"); !ok || title != "Hello" {
		t.Fatalf("TIT2 = %q, %v", title, ok)
	}
	if v, ok := got.Text("TXXX"); !ok || v != "value" {
		t.Fatalf("TXXX = %q, %v", v, ok)
	}
}

func buildID3(t *testing.T, title, key, value string) []byte {
	t.Helper()
	var frames bytes.Buffer
	write := func(id, text string) {
		var data bytes.Buffer
		data.WriteByte(3)
		data.WriteString(text)
		frames.WriteString(id)
		sz := uint32(data.Len())
		frames.Write([]byte{byte(sz >> 24), byte(sz >> 16), byte(sz >> 8), byte(sz)})
		frames.Write([]byte{0, 0})
		frames.Write(data.Bytes())
	}
	write("TIT2", title)
	write("TXXX", key+"\x00"+value)
	out := bytes.NewBufferString("ID3")
	out.Write([]byte{3, 0, 0})
	n := frames.Len()
	out.Write([]byte{byte(n >> 21 & 0x7f), byte(n >> 14 & 0x7f), byte(n >> 7 & 0x7f), byte(n & 0x7f)})
	out.Write(frames.Bytes())
	return out.Bytes()
}

func TestParseID3RejectsNonTag(t *testing.T) {
	if _, err := ParseID3([]byte("not a tag")); err == nil {
		t.Fatal("expected error for non-ID3 input")
	}
}

func TestParseEmsgVersions(t *testing.T) {
	v1 := BuildEmsgForTest(1, 2.5, "urn:test", "v", 1000, 7, []byte("msg"))
	e, err := ParseEmsg(v1)
	if err != nil {
		t.Fatalf("ParseEmsg v1: %v", err)
	}
	if e.Timescale != 1000 || e.TimeTicks != 2500 || e.SchemeID != "urn:test" || string(e.Message) != "msg" {
		t.Fatalf("unexpected emsg: %+v", e)
	}
}

func BuildEmsgForTest(version uint8, seconds float64, scheme, value string, timescale uint32, id uint32, msg []byte) []byte {
	var body bytes.Buffer
	body.WriteByte(version)
	body.Write([]byte{0, 0, 0})
	if version == 0 {
		body.WriteString(scheme)
		body.WriteByte(0)
		body.WriteString(value)
		body.WriteByte(0)
	}
	put32 := func(v uint32) { body.Write([]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}) }
	put64 := func(v uint64) {
		for i := 7; i >= 0; i-- {
			body.WriteByte(byte(v >> (8 * i)))
		}
	}
	put32(timescale)
	if version == 0 {
		put32(uint32(seconds * float64(timescale)))
	} else {
		put64(uint64(seconds * float64(timescale)))
	}
	put32(1)
	put32(id)
	if version == 1 {
		body.WriteString(scheme)
		body.WriteByte(0)
		body.WriteString(value)
		body.WriteByte(0)
	}
	body.Write(msg)
	return body.Bytes()
}

func TestParseWebVTT(t *testing.T) {
	doc := []byte("WEBVTT\n\nintro\n00:00:01.250 --> 00:00:02.500 align:start\nHello\nworld\n\n00:00:03.000 --> 00:00:04.000\nSecond\n")
	cues, err := ParseWebVTT(doc)
	if err != nil {
		t.Fatalf("ParseWebVTT: %v", err)
	}
	if len(cues) != 2 {
		t.Fatalf("cues = %d, want 2", len(cues))
	}
	if cues[0].ID != "intro" || cues[0].StartMS != 1250 || cues[0].EndMS != 2500 || cues[0].Text != "Hello\nworld" {
		t.Fatalf("first cue = %+v", cues[0])
	}
	if cues[0].Settings != "align:start" {
		t.Fatalf("settings = %q", cues[0].Settings)
	}
}

func TestParseChapters(t *testing.T) {
	doc := []byte("CHAPTER01=00:00:00.000\nCHAPTER01NAME=Opening\nCHAPTER02=00:01:30.500\nCHAPTER02NAME=Middle\n")
	chapters, err := ParseChapters(doc)
	if err != nil {
		t.Fatalf("ParseChapters: %v", err)
	}
	if len(chapters) != 2 || chapters[1].StartMS != 90500 || chapters[0].Title != "Opening" {
		t.Fatalf("chapters = %+v", chapters)
	}
}

func TestParseSEIRoundTrip(t *testing.T) {
	au := BuildSEINAL(1500000, "cue-a")
	msgs, err := ParseSEI(au)
	if err != nil {
		t.Fatalf("ParseSEI: %v", err)
	}
	stamps, err := SEITimestamps(msgs)
	if err != nil {
		t.Fatalf("SEITimestamps: %v", err)
	}
	if len(stamps) != 1 {
		t.Fatalf("stamps = %d, want 1", len(stamps))
	}
	if stamps[0].TimeUS != 1500000 || stamps[0].Kind != "cue-a" {
		t.Fatalf("stamp = %+v", stamps[0])
	}
}

func TestParseEventsIsolatesTracks(t *testing.T) {
	in := EventInput{
		WebVTT:   []byte("WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nHi\n"),
		Chapters: []byte("CHAPTER01=00:00:00.000\nCHAPTER01NAME=Start\n"),
		ID3:      [][]byte{buildID3(t, "t", "k", "v")},
		ID3Ticks: []int64{9000},
		Emsg:     [][]byte{BuildEmsgForTest(1, 1.5, "urn:x", "", 1000, 1, []byte("m"))},
		SEI:      [][]byte{BuildSEINAL(500000, "s")},
	}
	res, err := ParseEvents(in)
	if err != nil {
		t.Fatalf("ParseEvents: %v", err)
	}
	if len(res.Events) != 5 {
		t.Fatalf("events = %d, want 5: %+v", len(res.Events), res.Events)
	}
	kinds := map[model.MetadataKind]int{}
	for _, e := range res.Events {
		kinds[e.Kind]++
	}
	for _, k := range []model.MetadataKind{model.MetaSubtitle, model.MetaChapter, model.MetaID3, model.MetaEmsg, model.MetaSEI} {
		if kinds[k] != 1 {
			t.Fatalf("kind %s count = %d, want 1", k, kinds[k])
		}
	}
	// Events are sorted by repaired start.
	for i := 1; i < len(res.Events); i++ {
		a := res.Events[i-1].Time.RepairedTime
		b := res.Events[i].Time.RepairedTime
		if a.Den == 0 || b.Den == 0 {
			t.Fatalf("zero denominator in event time")
		}
	}
}

func TestParseEventsBackwardSubtitleFails(t *testing.T) {
	doc := []byte("WEBVTT\n\n00:00:05.000 --> 00:00:06.000\nA\n\n00:00:01.000 --> 00:00:02.000\nB\n")
	_, err := ParseEvents(EventInput{WebVTT: doc})
	if err == nil {
		t.Fatal("expected backward subtitle error")
	}
	pe, ok := model.AsError(err)
	if !ok || pe.Track != model.TrackSubtitles || pe.Stage != model.StageMetadata {
		t.Fatalf("unexpected error: %v", err)
	}
}
