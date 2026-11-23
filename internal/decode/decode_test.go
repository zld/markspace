package decode_test

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"medfix/internal/decode"
	"medfix/internal/fixture"
	"medfix/internal/timeline"
)

type sink struct {
	tracks []timeline.Track
	events []timeline.Event
	losses []string
}

func (s *sink) OnTrack(t timeline.Track) { s.tracks = append(s.tracks, t) }
func (s *sink) OnEvent(e timeline.Event) { s.events = append(s.events, e) }
func (s *sink) OnLoss(id string, seq uint16, off int64) {
	s.losses = append(s.losses, id)
}

func TestParseMP4MultiTrackRoundTrip(t *testing.T) {
	in := fixture.GenerateMP4(42, []fixture.MP4TrackSpec{
		{TrackID: 1, Timescale: 90000, Samples: 24, Delta: 3000, KeyEvery: 6, PayloadBase: 10},
		{TrackID: 2, Timescale: 48000, Samples: 48, Delta: 1024, KeyEvery: 0, PayloadBase: 70},
	})
	s := &sink{}
	if err := decode.ParseMP4(bytes.NewReader(in.Bytes), "src", s); err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	// Two media tracks plus the synthetic emsg metadata track.
	if len(s.tracks) != 3 {
		t.Fatalf("tracks=%d want 3", len(s.tracks))
	}
	// 24 + 48 samples + 1 emsg.
	if len(s.events) != 24+48+1 {
		t.Fatalf("events=%d want %d", len(s.events), 24+48+1)
	}
	// Every event must carry provenance and a content hash.
	for _, ev := range s.events {
		if ev.ContentHash == "" || ev.Offset.Begin < 0 || ev.Offset.End <= ev.Offset.Begin {
			t.Fatalf("event missing provenance: %+v", ev)
		}
		if !bytes.Equal(ev.RawPayload, in.Bytes[ev.Offset.Begin:ev.Offset.End]) && ev.Kind != timeline.KindEmsg {
			t.Fatalf("payload bytes do not match source offset: %s", ev.Kind)
		}
	}
	// Keyframes only on track 1, exactly 4.
	var keys int
	for _, ev := range s.events {
		if ev.Keyframe {
			keys++
			if !strings.HasSuffix(ev.TrackID, "/t1") {
				t.Fatalf("keyframe on wrong track: %s", ev.TrackID)
			}
		}
	}
	if keys != 4 {
		t.Fatalf("keyframes=%d want 4", keys)
	}
	// Original timescales preserved.
	scales := map[uint32]bool{}
	for _, tr := range s.tracks {
		scales[tr.Timescale] = true
	}
	if !scales[90000] || !scales[48000] {
		t.Fatalf("timescales not preserved: %v", scales)
	}
	// emsg parsed with bytes.
	var emsg int
	for _, ev := range s.events {
		if ev.Kind == timeline.KindEmsg {
			emsg++
			if string(ev.RawPayload) != "event-payload" {
				t.Fatalf("emsg payload=%q", ev.RawPayload)
			}
		}
	}
	if emsg != 1 {
		t.Fatalf("emsg count=%d", emsg)
	}
}

func TestParseMP4TruncatedSampleTable(t *testing.T) {
	in := fixture.GenerateMP4(7, []fixture.MP4TrackSpec{
		{TrackID: 1, Timescale: 90000, Samples: 12, Delta: 3000, KeyEvery: 3, PayloadBase: 1},
	})
	// Cut inside the moov box so a sample table is truncated.
	cut := len(in.Bytes) - 40
	corrupt := fixture.TruncateAt(in.Bytes, cut)
	s := &sink{}
	err := decode.ParseMP4(bytes.NewReader(corrupt), "src", s)
	var te *timeline.Error
	if !errors.As(err, &te) {
		t.Fatalf("want timeline.Error, got %v", err)
	}
	if te.Code != timeline.CodeTruncatedBox {
		t.Fatalf("code=%s want %s", te.Code, timeline.CodeTruncatedBox)
	}
	// Fault must still locate the raw input offset.
	if te.Offset < 0 || te.Offset > int64(cut) {
		t.Fatalf("bad offset in error: %d", te.Offset)
	}
}

func TestParseMP4TruncatedMdat(t *testing.T) {
	in := fixture.GenerateMP4(11, []fixture.MP4TrackSpec{
		{TrackID: 1, Timescale: 1000, Samples: 8, Delta: 100, KeyEvery: 2, PayloadBase: 5},
	})
	// Remove the last 10 bytes: last mdat sample is cut.
	corrupt := fixture.TruncateAt(in.Bytes, len(in.Bytes)-10)
	s := &sink{}
	err := decode.ParseMP4(bytes.NewReader(corrupt), "src", s)
	var te *timeline.Error
	if !errors.As(err, &te) {
		t.Fatalf("want timeline.Error for truncated mdat, got %v", err)
	}
	if te.Stage != "parse" || te.Offset < 0 {
		t.Fatalf("error lost stage/offset: %+v", te)
	}
}

func TestParseRTPID3AndLoss(t *testing.T) {
	in := fixture.GenerateRTP(99, fixture.RTPSpec{
		ClockRate: 90000, Packets: 20, StartSeq: 100,
		StartTS: 5000, TSIncrement: 3000, ID3Every: 5, LossSeq: []uint16{103, 104},
	})
	s := &sink{}
	p := decode.NewRTPParser("cap", 90000)
	if err := p.Parse(bytes.NewReader(in.Bytes), s); err != nil {
		t.Fatal(err)
	}
	if len(s.events) != 18 {
		t.Fatalf("events=%d want 18", len(s.events))
	}
	if len(s.losses) != 2 {
		t.Fatalf("loss reports=%d want 2", len(s.losses))
	}
	var id3 int
	for _, ev := range s.events {
		if ev.Kind == timeline.KindID3 {
			id3++
			if !bytes.HasPrefix(ev.RawPayload, []byte("ID3")) {
				t.Fatal("ID3 payload prefix lost")
			}
		}
		if ev.Offset.Begin < 0 || ev.ContentHash == "" {
			t.Fatal("RTP event missing provenance")
		}
	}
	if id3 != 4 { // indices 0,5,10,15
		t.Fatalf("ID3 packets=%d want 4", id3)
	}
}

func TestParseRTPTruncatedFrame(t *testing.T) {
	in := fixture.GenerateRTP(3, fixture.RTPSpec{
		ClockRate: 48000, Packets: 6, StartSeq: 0, StartTS: 0, TSIncrement: 480,
	})
	corrupt := fixture.TruncateAt(in.Bytes, len(in.Bytes)-3)
	s := &sink{}
	p := decode.NewRTPParser("cap", 48000)
	err := p.Parse(bytes.NewReader(corrupt), s)
	var te *timeline.Error
	if !errors.As(err, &te) {
		t.Fatalf("want timeline.Error, got %v", err)
	}
	if te.Code != timeline.CodePacketLoss && te.Code != timeline.CodeTruncatedBox {
		t.Fatalf("unexpected code: %s", te.Code)
	}
	if te.Offset < 0 {
		t.Fatal("truncated frame error lost offset")
	}
}

func TestParseRTPTruncatedID3NotSilentlySkipped(t *testing.T) {
	// Header claims a 0x7F (127) byte body but the packet only has 10.
	payload := []byte{'I', 'D', '3', 4, 0, 0, 0, 0, 0, 0x7F}
	_, _, err := decode.ParseID3Header(payload, 1234)
	var te *timeline.Error
	if !errors.As(err, &te) {
		t.Fatalf("want structured error, got %v", err)
	}
	if te.Offset != 1234 {
		t.Fatalf("offset=%d", te.Offset)
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("must wrap io.ErrUnexpectedEOF: %v", err)
	}
}

func TestParseRTPReorderingDoesNotAdvanceState(t *testing.T) {
	in := fixture.GenerateRTP(5, fixture.RTPSpec{
		ClockRate: 90000, Packets: 8, StartSeq: 0, StartTS: 0,
		TSIncrement: 3000, Shuffle: true,
	})
	s := &sink{}
	p := decode.NewRTPParser("cap", 90000)
	if err := p.Parse(bytes.NewReader(in.Bytes), s); err != nil {
		t.Fatalf("reordering should parse: %v", err)
	}
	if p.Rollovers() != 0 {
		t.Fatal("reordering must not be counted as rollover")
	}
}
