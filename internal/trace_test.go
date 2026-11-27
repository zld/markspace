package rtpreplay

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pion/rtp"
)

func marshalRTP(t *testing.T, seq uint16, ts uint32, marker bool, payload []byte) string {
	t.Helper()
	p := &rtp.Packet{
		Header:  rtp.Header{Version: 2, PayloadType: 111, SequenceNumber: seq, Timestamp: ts, Marker: marker},
		Payload: payload,
	}
	b, err := p.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func miniTrace(t *testing.T) *Trace {
	t.Helper()
	return &Trace{
		Version: "1",
		Seed:    17,
		Clock:   TraceClock{RTPRateHz: 48000, EventRateHz: 1000},
		Streams: []Stream{{
			Track: "opus-main", Codec: CodecOpus, ClockHz: 48000, DeclaredDurationMs: 40,
			Packets: []TracePacket{
				{RTPHex: marshalRTP(t, 1, 0, false, []byte{0x78, 0x02}), ArrivalMs: 20},
				{RTPHex: marshalRTP(t, 2, 960, true, []byte{0x78, 0x02}), ArrivalMs: 40},
			},
		}},
		Events: []TimedEvent{{AtMs: 0, Kind: EventBandwidth, ValueBps: 32000}},
	}
}

func TestParseTraceRoundTrip(t *testing.T) {
	tr := miniTrace(t)
	b, _ := json.Marshal(tr)
	got, err := ParseTrace(b)
	if err != nil {
		t.Fatal(err)
	}
	if got.InputDigest != tr.Digest() {
		t.Fatal("digest not stable across parse")
	}
	if len(got.Streams) != 1 || len(got.Streams[0].Packets) != 2 {
		t.Fatalf("streams=%+v", got.Streams)
	}
	p := got.Streams[0].Packets[1]
	if p.Seq != 2 || p.Timestamp != 960 || !p.Marker || len(p.Payload) != 2 {
		t.Fatalf("packet=%+v", p)
	}
	if p.ArrivalMs != 40 {
		t.Fatalf("arrival=%d", p.ArrivalMs)
	}
}

func TestParseTraceDigestIndependentOfEventOrder(t *testing.T) {
	a := miniTrace(t)
	a.Events = append(a.Events, TimedEvent{AtMs: 100, Kind: EventBandwidth, ValueBps: 64000})
	b := miniTrace(t)
	b.Events = []TimedEvent{{AtMs: 100, Kind: EventBandwidth, ValueBps: 64000}, {AtMs: 0, Kind: EventBandwidth, ValueBps: 32000}}
	if a.Digest() != b.Digest() {
		t.Fatal("digest must be order-independent over events")
	}
}

func TestParseTraceRejectsUnknownField(t *testing.T) {
	tr := miniTrace(t)
	b, _ := json.Marshal(tr)
	b = []byte(strings.Replace(string(b), `"seed":17`, `"seed":17,"bogus":1`, 1))
	if _, err := ParseTrace(b); err == nil {
		t.Fatal("expected unknown field error")
	}
}

func TestParseTraceValidation(t *testing.T) {
	cases := map[string]func(*Trace){
		"no streams":      func(tr *Trace) { tr.Streams = nil },
		"zero clock":      func(tr *Trace) { tr.Clock.RTPRateHz = 0 },
		"bad codec":       func(tr *Trace) { tr.Streams[0].Codec = "vorbis" },
		"bad hex":         func(tr *Trace) { tr.Streams[0].Packets[0].RTPHex = "zz" },
		"bad rtp":         func(tr *Trace) { tr.Streams[0].Packets[0].RTPHex = "00" },
		"neg arrival":     func(tr *Trace) { tr.Streams[0].Packets[0].ArrivalMs = -1 },
		"bad event kind":  func(tr *Trace) { tr.Events[0].Kind = "latency" },
		"zero bandwidth":  func(tr *Trace) { tr.Events[0].ValueBps = 0 },
		"mp4 without sum": func(tr *Trace) { tr.MP4 = &MP4Ref{FileName: "a.mp4"} },
	}
	for name, mutate := range cases {
		tr := miniTrace(t)
		mutate(tr)
		b, _ := json.Marshal(tr)
		if _, err := ParseTrace(b); err == nil {
			t.Fatalf("%s: expected error", name)
		}
	}
}
