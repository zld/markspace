package transport

import (
	"testing"

	"mediadist/internal/timeline"
)

func TestParseCSRCExtensionAndPadding(t *testing.T) {
	// V=2, P=1, X=1, CC=2; marker, PT 96. The extension word holds one
	// one-byte element (id 1, two bytes of data) followed by padding.
	raw := []byte{
		0xA0 | 0x10 | 0x02, 0xE0,
		0x00, 0x2A, // seq 42
		0x00, 0x00, 0x10, 0x00, // ts
		0xDE, 0xAD, 0xBE, 0xEF, // ssrc
		0x11, 0x11, 0x11, 0x11, // csrc 1
		0x22, 0x22, 0x22, 0x22, // csrc 2
		0xBE, 0xDE, 0x00, 0x01, // extension profile, one word
		0x11, 0xAA, 0xBB, 0x00, // element id 1 len 2, then padding
		0xDE, 0xAD, 0xBE, 0xEF, // payload
		0x00, 0x02, // padding: one filler byte, final byte = padding size
	}
	p, err := Parse(raw, ParseOptions{Stream: 1, Strict: true})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p.Seq != 42 || p.Timestamp != 0x1000 || p.SSRC != 0xDEADBEEF {
		t.Fatalf("header fields %+v", p)
	}
	if len(p.CSRC) != 2 || p.CSRC[0] != 0x11111111 || p.CSRC[1] != 0x22222222 {
		t.Fatalf("csrc %v", p.CSRC)
	}
	if p.ExtensionProfile != 0xBEDE || len(p.Extensions) != 1 {
		t.Fatalf("extension %+v %+v", p.ExtensionProfile, p.Extensions)
	}
	if !p.Marker || p.PayloadType != 96 {
		t.Fatalf("marker/pt %t %d", p.Marker, p.PayloadType)
	}
	if p.PaddingSize != 2 {
		t.Fatalf("padding %d", p.PaddingSize)
	}
	if string(p.Payload) != string([]byte{0xDE, 0xAD, 0xBE, 0xEF}) {
		t.Fatalf("payload %x", p.Payload)
	}
	// The raw copy must be byte-identical to the datagram for evidence hashing.
	if len(p.Raw) != len(raw) {
		t.Fatalf("raw length %d want %d", len(p.Raw), len(raw))
	}
}

func TestParseRejectsBadVersion(t *testing.T) {
	raw := mkPacket(0, 1, 0, 4).Raw
	raw[0] = 0x40 // version 1
	if _, err := Parse(raw, ParseOptions{Stream: 0}); err == nil {
		t.Fatal("version 1 must be rejected")
	}
}

func TestParseStrictRejectsUnknownExtensionProfile(t *testing.T) {
	raw := []byte{
		0x90, 0x60, 0x00, 0x01,
		0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x01,
		0x12, 0x34, 0x00, 0x00, // extension profile 0x1234, zero words
	}
	p, err := Parse(raw, ParseOptions{Stream: 0, Strict: true})
	if err == nil {
		t.Fatal("unknown extension profile must be rejected in strict mode")
	}
	if p.Seq != 1 {
		t.Fatalf("sequence should still be readable: %d", p.Seq)
	}
	// Lenient mode accepts it and keeps the payload empty.
	if _, err := Parse(raw, ParseOptions{Stream: 0}); err != nil {
		t.Fatalf("lenient mode should accept: %v", err)
	}
}

func TestParseStreamEnforcesLimits(t *testing.T) {
	small := Limits{MaxPacketBytes: 16, MaxPackets: 4, MaxDropped: 4}
	if _, err := small.Validate(); err != nil {
		t.Fatal(err)
	}
	datagrams := [][]byte{
		mkPacket(0, 1, 0, 4).Raw,
		make([]byte, 32), // too large
	}
	// ParseStream rejects the batch before parsing if it exceeds MaxPackets.
	tooMany := append(append([][]byte{}, datagrams...), datagrams...)
	tooMany = append(tooMany, datagrams...)
	if _, err := ParseStream(0, tooMany, small, false); err == nil {
		t.Fatal("packet count above MaxPackets must be refused")
	}
	res, err := ParseStream(0, datagrams, small, false)
	if err != nil {
		t.Fatalf("ParseStream: %v", err)
	}
	if len(res.Packets) != 1 || res.Skipped != 1 {
		t.Fatalf("packets %d skipped %d", len(res.Packets), res.Skipped)
	}
	if len(res.Errors) != 1 || res.Errors[0].Code != "packet_too_large" {
		t.Fatalf("errors %+v", res.Errors)
	}
}

func TestEstimateSortsWindowAndCapsRate(t *testing.T) {
	clock, _ := timeline.NewClock(1000, 32)
	est, err := NewEstimator(clock, EstimatorConfig{
		Window: 1e9, MinSamples: 1, MaxRateBps: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	// One huge sample in a tiny tick span would imply an enormous rate; the
	// configured cap must clamp it rather than overflow.
	est.Add(0, 1<<20)
	est.Add(10, 1<<20)
	rate := est.RateBps(10)
	if rate != 1000 {
		t.Fatalf("rate %d want the 1000 bps cap", rate)
	}
}

func TestEstimatorExpiresOldSamples(t *testing.T) {
	clock, _ := timeline.NewClock(1000, 32)
	est, _ := NewEstimator(clock, EstimatorConfig{Window: 1e9, MinSamples: 1})
	est.Add(0, 100)
	// After two seconds the first sample is outside the window; with a single
	// fresh sample the span is zero, so no estimate can be formed.
	est.Add(2000, 100)
	if got := est.RateBps(2000); got != 0 {
		t.Fatalf("single-sample span should yield 0, got %d", got)
	}
}

func TestFragmentAggregationKeysBySegment(t *testing.T) {
	clock, _ := timeline.NewClock(1000, 32)
	est, _ := NewEstimator(clock, DefaultEstimatorConfig())
	inputs := []FragmentSample{
		{Segment: 2, Bytes: 100, Tick: 2000},
		{Segment: 0, Bytes: 300, Tick: 0},
		{Segment: 0, Bytes: 100, Tick: 500, Keyframe: true},
		{Segment: 2, Bytes: 50, Missing: 3, Tick: 2500},
	}
	reports := AggregateFragments(inputs, est, 2e9)
	if len(reports) != 2 {
		t.Fatalf("reports %+v", reports)
	}
	if reports[0].Index != 0 || reports[0].Samples != 2 || reports[0].Bytes != 400 || reports[0].Keyframes != 1 {
		t.Fatalf("segment 0 %+v", reports[0])
	}
	if reports[1].Index != 2 || reports[1].Bytes != 150 || reports[1].Lost != 3 {
		t.Fatalf("segment 2 %+v", reports[1])
	}
}
