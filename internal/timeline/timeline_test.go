package timeline

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/pion/rtp"
)

func toHex(b []byte) string { return hex.EncodeToString(b) }

func TestClockConversions(t *testing.T) {
	c, err := NewClock(1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.MediaToNS(1000); got != 1_000_000_000 {
		t.Fatalf("media ns = %d", got)
	}
	if _, err := NewClock(0); err == nil {
		t.Fatal("zero scale must error")
	}
}

func TestRTPWrappingAndRate(t *testing.T) {
	pkts := []RTPLogEntry{
		{SSRC: 7, Timestamp: 0xFFFFFFF0, Sequence: 1, RecvNS: 0},
		{SSRC: 7, Timestamp: 0x00000010, Sequence: 2, RecvNS: 0},
	}
	s, err := NewRTPStream(7, 48000, pkts)
	if err != nil {
		t.Fatal(err)
	}
	// 32 ticks forward across the 32-bit wrap boundary.
	want := int64(32) * NanosecondsPerSecond / 48000
	if got := s.TimeNS(0x00000010); got != want {
		t.Fatalf("wrap ns = %d want %d", got, want)
	}
	if got := s.TimeNS(0xFFFFFFF0); got != 0 {
		t.Fatalf("anchor ns = %d", got)
	}
}

func TestRTPPacketRoundTrip(t *testing.T) {
	orig := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			SSRC:           4242,
			Timestamp:      123456,
			SequenceNumber: 9,
			PayloadType:    111,
		},
		Payload: []byte{0xDE, 0xAD, 0xBE, 0xEF},
	}
	raw, err := orig.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	p, err := DecodeHexPacket(toHex(raw))
	if err != nil {
		t.Fatal(err)
	}
	if p.SSRC != 4242 || p.Timestamp != 123456 || p.SequenceNumber != 9 {
		t.Fatalf("parsed header wrong: %+v", p.Header)
	}
	entry := EntryFromPacket(p, 999)
	if entry.PayloadLen != 4 || entry.RecvNS != 999 {
		t.Fatalf("entry wrong: %+v", entry)
	}
}

func TestValidateOrdering(t *testing.T) {
	good := []RTPLogEntry{{Timestamp: 10}, {Timestamp: 20}, {Timestamp: 30}}
	if err := ValidateOrdering(good); err != nil {
		t.Fatalf("ordered stream flagged: %v", err)
	}
	bad := []RTPLogEntry{{Timestamp: 30, Sequence: 1}, {Timestamp: 20, Sequence: 2}}
	if err := ValidateOrdering(bad); err == nil {
		t.Fatal("regression not detected")
	}
}

func TestLoadEvents(t *testing.T) {
	src := `{"kind":"event","at_ns":0,"track":1,"label":"start"}
{"kind":"ssrc_map","ssrc":4242,"track":1,"clock_rate":48000}

{"kind":"rtp","at_ns":20833,"ssrc":4242,"rtp_ts":1,"seq":1,"packet_hex":"800000000000109200000000"}
`
	evs, err := LoadEvents(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 3 {
		t.Fatalf("want 3 records, got %d", len(evs))
	}
	if evs[0].Line != 1 || evs[2].Line != 4 {
		t.Fatalf("line tracking wrong: %+v", evs)
	}
	if _, err := LoadEvents(strings.NewReader("{not json")); err == nil {
		t.Fatal("malformed line must error")
	}
}

func TestAlignHappyPath(t *testing.T) {
	blocks := []BlockView{
		{TrackNo: 1, TimeNS: 0},
		{TrackNo: 2, TimeNS: 0},
		{TrackNo: 1, TimeNS: 1_000_000_000},
		{TrackNo: 2, TimeNS: 1_000_000_000},
	}
	events := []Event{
		{Kind: KindEvent, AtNS: 100, Track: 1, Line: 1},
		{Kind: KindEvent, AtNS: 200, Track: 2, Line: 2},
		{Kind: KindEvent, AtNS: 1_000_000_250, Track: 1, Line: 3},
		{Kind: KindEvent, AtNS: 1_000_000_500, Track: 2, Line: 4},
	}
	ms := Align(blocks, events, 1_000_000)
	if len(ms) != 4 {
		t.Fatalf("want 4 mappings, got %d", len(ms))
	}
	for i, m := range ms {
		if m.Gap != GapNone {
			t.Fatalf("mapping %d unexpected gap %s: %s", i, m.Gap, m.Detail)
		}
	}
	if got := MaxTimeError(ms); got != 500 {
		t.Fatalf("max residual = %d want 500", got)
	}
}

func TestAlignGapsNoGuessing(t *testing.T) {
	blocks := []BlockView{
		{TrackNo: 1, TimeNS: 1000},
		{TrackNo: 1, TimeNS: 500}, // regression
		{TrackNo: 9, TimeNS: 2000}, // no event for track
	}
	events := []Event{{Kind: KindEvent, AtNS: 1000, Track: 1, Line: 1}}
	ms := Align(blocks, events, 1_000_000)
	if ms[0].Gap != GapNone {
		t.Fatalf("first mapping should be clean: %+v", ms[0])
	}
	if ms[1].Gap != GapRegression {
		t.Fatalf("regression not marked: %+v", ms[1])
	}
	if ms[1].EventAtNS != 0 || ms[1].EventLine != -1 {
		t.Fatal("regression mapping must not invent an event")
	}
	if ms[2].Gap != GapNoEvent {
		t.Fatalf("missing event not marked: %+v", ms[2])
	}
}
