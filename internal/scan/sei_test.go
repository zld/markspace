package scan

import (
	"testing"

	"github.com/pion/rtp"
)

// marshalPacket builds a raw RTP packet for test fixtures.
func marshalPacket(pt uint8, seq uint16, ts uint32, payload []byte) []byte {
	p := &rtp.Packet{
		Header: rtp.Header{
			Version: 2, PayloadType: pt, SequenceNumber: seq,
			Timestamp: ts, SSRC: 1, Marker: true,
		},
		Payload: payload,
	}
	raw, err := p.Marshal()
	if err != nil {
		panic(err)
	}
	return raw
}

// h264SEINAL builds an SEI NAL unit (type 6) with one user-data message.
func h264SEINAL() []byte {
	// header(6) + payloadType(5=user data unregistered) + size + body + rbsp stop
	sei := []byte{6, 5, 3, 0xAA, 0xBB, 0xCC, 0x80}
	return sei
}

func TestRTPH264SingleSEI(t *testing.T) {
	x := NewRTPSEIExtractor("h264", 7)
	nal := h264SEINAL()
	raw := marshalPacket(96, 100, 90000, nal) // ts => 1.0 s
	if err := x.Push(raw, 0); err != nil {
		t.Fatal(err)
	}
	if len(x.Events) != 1 {
		t.Fatalf("events = %d, anomalies=%+v", len(x.Events), x.Anomalies)
	}
	ev := x.Events[0]
	if ev.Kind != EventSEI || ev.TrackID != 7 || ev.Tick != 90000 {
		t.Fatalf("event wrong: %+v", ev)
	}
	if ev.TimeBase.Den != 90000 || ev.Offset != 0 {
		t.Fatalf("timebase/offset wrong: %+v", ev)
	}
	ns, err := ev.TimeBase.ToNanos(ev.Tick)
	if err != nil || ns != 1_000_000_000 {
		t.Fatalf("sei nanos = %d err=%v", ns, err)
	}
}

func TestRTPH264FUA(t *testing.T) {
	x := NewRTPSEIExtractor("h264", 1)
	nal := h264SEINAL()
	// FU indicator: type 28, NRI from original (0 here). FU-A header carries
	// the real type 6 with start/end bits.
	ind := byte(28) // NRI=0, type=28
	mkFU := func(start, end bool, body []byte) []byte {
		var h byte = 6
		if start {
			h |= 0x80
		}
		if end {
			h |= 0x40
		}
		return append([]byte{ind, h}, body...)
	}
	p1 := marshalPacket(96, 1, 1000, mkFU(true, false, nal[1:4]))
	p2 := marshalPacket(96, 2, 1000, mkFU(false, true, nal[4:]))
	_ = x.Push(p1, 0)
	_ = x.Push(p2, 100)
	if len(x.Events) != 1 {
		t.Fatalf("reassembled SEI events = %d anomalies=%+v", len(x.Events), x.Anomalies)
	}
	if string(x.Events[0].Payload) != string(nal) {
		t.Fatalf("reassembly mismatch: %v vs %v", x.Events[0].Payload, nal)
	}
}

func TestRTPH264OrderingAnomalies(t *testing.T) {
	x := NewRTPSEIExtractor("h264", 1)
	nal := h264SEINAL()
	// In-order seq 10 then duplicate 10, then backwards timestamp.
	_ = x.Push(marshalPacket(96, 10, 1000, nal), 0)
	_ = x.Push(marshalPacket(96, 10, 2000, nal), 50) // duplicate seq
	_ = x.Push(marshalPacket(96, 11, 500, nal), 100) // timestamp backstep
	var dup, wrap int
	for _, a := range x.Anomalies {
		switch a.Type {
		case "DUPLICATE_EVENT":
			dup++
		case "TIMEBASE_WRAP":
			wrap++
		}
		if a.SourceOffset < 0 || a.RawTick == 0 {
			t.Fatalf("anomaly lost offset/raw tick: %+v", a)
		}
	}
	if dup != 1 || wrap != 1 {
		t.Fatalf("dup=%d wrap=%d anomalies=%+v", dup, wrap, x.Anomalies)
	}
}

func TestRTPH265PrefixSEI(t *testing.T) {
	x := NewRTPSEIExtractor("h265", 4)
	// H.265 prefix SEI: type 39 => first header byte (39<<1)=0x4E, second 0.
	nal := []byte{0x4E, 0x00, 5, 2, 0xAB, 0xCD, 0x80}
	_ = x.Push(marshalPacket(98, 1, 45000, nal), 0) // 0.5 s
	if len(x.Events) != 1 {
		t.Fatalf("h265 events = %d anomalies=%+v", len(x.Events), x.Anomalies)
	}
	if x.Events[0].Tick != 45000 {
		t.Fatalf("h265 tick = %d", x.Events[0].Tick)
	}
}

func TestRTPBadPacketAnomaly(t *testing.T) {
	x := NewRTPSEIExtractor("h264", 1)
	// Truncated RTP header must produce an anomaly, not an error.
	if err := x.Push([]byte{0x80}, 7); err != nil {
		t.Fatalf("Push should swallow parse errors: %v", err)
	}
	if len(x.Anomalies) != 1 || x.Anomalies[0].SourceOffset != 7 {
		t.Fatalf("expected unparseable anomaly at offset 7, got %+v", x.Anomalies)
	}
}

func TestRTPUnsupportedCodec(t *testing.T) {
	x := NewRTPSEIExtractor("av1", 1)
	err := x.Push(marshalPacket(96, 1, 0, []byte{0}), 0)
	if err == nil {
		t.Fatal("unsupported codec must error")
	}
}
